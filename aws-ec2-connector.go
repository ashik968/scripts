package main

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	tea "github.com/charmbracelet/bubbletea"

	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"golang.design/x/clipboard"
)

type InstanceInfo struct {
	ID        string
	Name      string
	PrivateIP string
	Platform  string
}

type instanceSelectorModel struct {
	instances []InstanceInfo
	cursor    int
	selected  map[int]struct{}
}

func (m instanceSelectorModel) Init() tea.Cmd {
	return nil
}

func (m instanceSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.instances)-1 {
				m.cursor++
			}
		case " ":
			_, ok := m.selected[m.cursor]
			if ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		case "enter":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m instanceSelectorModel) View() string {
	s := "📋 Select instances (space to toggle, enter to confirm, q to quit):\n\n"
	for i, inst := range m.instances {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, ok := m.selected[i]; ok {
			checked = "x"
		}

		platform := "Linux"
		if inst.Platform != "" {
			platform = inst.Platform
		}
		s += fmt.Sprintf("%s [%s] %-20s | %s | %s | %s\n", cursor, checked, inst.Name, inst.ID, inst.PrivateIP, platform)
	}
	return s
}

func selectInstances(instances []InstanceInfo) ([]InstanceInfo, error) {
	m := instanceSelectorModel{
		instances: instances,
		selected:  make(map[int]struct{}),
	}
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("TUI failed: %v", err)
	}
	m = finalModel.(instanceSelectorModel)

	var selectedInstances []InstanceInfo
	for i := range m.selected {
		selectedInstances = append(selectedInstances, m.instances[i])
	}

	if len(selectedInstances) == 0 {
		return nil, fmt.Errorf("no instances selected")
	}

	return selectedInstances, nil
}

func main() {
	region := flag.String("region", "ap-northeast-1", "AWS region (default: Tokyo)")
	profile := flag.String("profile", "", "AWS profile to use")
	rdpPort := flag.String("rdp-port", "9000", "Local port for RDP connection")
	tags := flag.String("tags", "", "Comma-separated tags to filter instances (e.g., 'Name=web-server,Env=prod')")
	pemKeyPath := flag.String("pem-key-path", "", "Path to PEM private key for Windows instances")
	flag.Parse()

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(*region),
	}
	if *profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(*profile))
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(), opts...)
	if err != nil {
		log.Fatalf("❌ Unable to load AWS config: %v", err)
	}

	tagFilters, err := parseTags(*tags)
	if err != nil {
		log.Fatalf("❌ Invalid tags format: %v", err)
	}

	instances, err := listRunningInstances(cfg, tagFilters)
	if err != nil {
		log.Fatalf("❌ Failed to list instances: %v", err)
	}

	if len(instances) == 0 {
		log.Fatal("🚫 No running EC2 instances found in this region.")
	}

	selected, err := selectInstances(instances)
	if err != nil {
		log.Fatalf("❌ Failed to select instances: %v", err)
	}

	boldPrint(fmt.Sprintf("✅ Selected %d instance(s)", len(selected)))

	if len(selected) > 0 {
		var wg sync.WaitGroup
		rdpPortBase, err := strconv.Atoi(*rdpPort)
		if err != nil {
			log.Fatalf("❌ Invalid rdp-port value: %s", *rdpPort)
		}
		windowsInstanceCount := 0

		var keyPath string
		containsWindows := false
		for _, inst := range selected {
			if inst.Platform == "windows" {
				containsWindows = true
				break
			}
		}

		if containsWindows {
			if *pemKeyPath != "" {
				keyPath = *pemKeyPath
			} else {
				fmt.Print("🔑 Enter full path to your Windows EC2 PEM private key: ")
				keyPath = readLine()
			}
		}

		for _, instance := range selected {
			wg.Add(1)

			portToUse := ""
			if instance.Platform == "windows" {
				portToUse = strconv.Itoa(rdpPortBase + windowsInstanceCount)
				windowsInstanceCount++
			}

			go func(inst InstanceInfo, port string, keyPath string) {
				defer wg.Done()

				boldPrint("Initiating connection to instance: " + inst.Name + " (" + inst.ID + ")")
				var sessionErr error
				if inst.Platform == "windows" {
					sessionErr = handleWindowsConnection(cfg, inst.ID, *region, port, keyPath)
				} else {
					sessionErr = handleLinuxConnection(inst.ID, *region)
				}
				if sessionErr != nil {
					if !strings.Contains(sessionErr.Error(), "SSM agent not connected") {
						log.Printf("❌ Session failed for %s: %v", inst.ID, sessionErr)
					}
				}
			}(instance, portToUse, keyPath)
		}
		wg.Wait()
		boldPrint("✅ All sessions terminated.")
	}
}

func handleWindowsConnection(cfg aws.Config, instanceID, region, rdpPort, keyPath string) error {
	password, err := getWindowsPassword(cfg, instanceID, keyPath)
	if err != nil {
		return fmt.Errorf("failed to get Windows password: %v", err)
	}

	_ = copyToClipboard(password)
	yellowBoldPrint("🔐 Windows Administrator password for " + instanceID + " (copied to clipboard): " + password)

	return startPortForward(instanceID, region, rdpPort)
}

func handleLinuxConnection(instanceID, region string) error {
	return startShellSession(instanceID, region)
}

func parseTags(tagsStr string) ([]ec2types.Filter, error) {
	if tagsStr == "" {
		return nil, nil
	}
	var filters []ec2types.Filter
	pairs := strings.Split(tagsStr, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid tag format: %s. Expected key=value", pair)
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid tag format: %s. Key and value cannot be empty", pair)
		}
		filters = append(filters, ec2types.Filter{
			Name:   aws.String("tag:" + key),
			Values: []string{value},
		})
	}
	return filters, nil
}

func listRunningInstances(cfg aws.Config, tagFilters []ec2types.Filter) ([]InstanceInfo, error) {
	ec2Client := ec2.NewFromConfig(cfg)
	ssmClient := ssm.NewFromConfig(cfg)

	// Get all SSM managed instance IDs
	ssmIDs := map[string]bool{}
	ssmPaginator := ssm.NewDescribeInstanceInformationPaginator(ssmClient, &ssm.DescribeInstanceInformationInput{})
	for ssmPaginator.HasMorePages() {
		page, err := ssmPaginator.NextPage(context.TODO())
		if err != nil {
			return nil, fmt.Errorf("failed to get SSM instance info: %v", err)
		}
		for _, info := range page.InstanceInformationList {
			ssmIDs[*info.InstanceId] = true
		}
	}

	// Get all running EC2 instances
	filters := []ec2types.Filter{
		{
			Name:   aws.String("instance-state-name"),
			Values: []string{"running"},
		},
	}
	if tagFilters != nil {
		filters = append(filters, tagFilters...)
	}
	out, err := ec2Client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{
		Filters: filters,
	})
	if err != nil {
		return nil, err
	}

	var instances []InstanceInfo
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			name := "(no name)"
			for _, tag := range inst.Tags {
				if *tag.Key == "Name" {
					name = *tag.Value
					break
				}
			}
			// Skip instances with "Storage-Gateway" in the name
			if strings.Contains(name, "Storage-Gateway") {
				continue
			}
			// Only include if instance is managed by SSM
			if _, ok := ssmIDs[*inst.InstanceId]; !ok {
				continue
			}
			ip := "-"
			if inst.PrivateIpAddress != nil {
				ip = *inst.PrivateIpAddress
			}
			platform := ""
			if inst.Platform != "" {
				platform = strings.ToLower(string(inst.Platform))
			}
			instances = append(instances, InstanceInfo{
				ID:        *inst.InstanceId,
				Name:      name,
				PrivateIP: ip,
				Platform:  platform,
			})
		}
	}
	return instances, nil
}

func startPortForward(instanceID, region, port string) error {
	for {
		if isPortInUse(port) {
			fmt.Printf("❗ Port %s is already in use. Killing process using it...\n", port)
			err := killProcessOnPort(port)
			if err != nil {
				return fmt.Errorf("failed to kill process on port %s: %v", port, err)
			}
			fmt.Printf("✅ Killed process using port %s. Retrying...\n", port)
			time.Sleep(1 * time.Second)
		} else {
			break
		}
	}

	boldPrint("📡 Starting port forwarding to local port " + port + "...")

	cmd := exec.Command("aws", "ssm", "start-session",
		"--target", instanceID,
		"--document-name", "AWS-StartPortForwardingSession",
		"--parameters", fmt.Sprintf("portNumber=3389,localPortNumber=%s", port),
		"--region", region,
	)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("⏳ Waiting for port to be ready...")
	time.Sleep(3 * time.Second)

	fmt.Printf("➡️  Port forwarding started. Connect your RDP client to localhost:%s\n", port)
	fmt.Println("Press Ctrl+C to stop port forwarding.")

	// Run in foreground, so Ctrl+C will stop it
	err := cmd.Run()
	if err != nil {
		// Enhanced error handling for SSM agent not connected
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "TargetNotConnected") {
				fmt.Println("❌ SSM session failed: The instance is not connected to SSM.")
				fmt.Println("👉 Please check if the SSM agent is running and properly configured on this instance.")
				return fmt.Errorf("SSM agent not connected on instance %s", instanceID)
			}
		}
		return fmt.Errorf("%v", err)
	}
	return nil
}

func killProcessOnPort(port string) error {
	cmd := exec.Command("lsof", "-t", "-i", fmt.Sprintf(":%s", port))
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("lsof failed: %v", err)
	}
	pids := strings.Fields(string(output))
	for _, pid := range pids {
		killCmd := exec.Command("kill", "-9", pid)
		if err := killCmd.Run(); err != nil {
			return fmt.Errorf("failed to kill pid %s: %v", pid, err)
		}
	}
	return nil
}

func isPortInUse(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func readLine() string {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	return scanner.Text()
}

func boldPrint(s string) {
	fmt.Println("\033[1m" + s + "\033[0m")
}

func yellowBoldPrint(s string) {
	fmt.Println("\033[1;33m" + s + "\033[0m")
}

func copyToClipboard(s string) error {
	if err := clipboard.Init(); err != nil {
		return err
	}
	clipboard.Write(clipboard.FmtText, []byte(s))
	return nil
}

func getWindowsPassword(cfg aws.Config, instanceID, keyPath string) (string, error) {
	client := ec2.NewFromConfig(cfg)
	out, err := client.GetPasswordData(context.TODO(), &ec2.GetPasswordDataInput{
		InstanceId: aws.String(instanceID),
	})
	if err != nil {
		return "", err
	}
	if out.PasswordData == nil || *out.PasswordData == "" {
		return "", fmt.Errorf("Password data not available yet")
	}

	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("Failed to read key file: %v", err)
	}

	privKey, err := sshParsePrivateKey(pemBytes)
	if err != nil {
		return "", err
	}

	password, err := decryptPassword(*out.PasswordData, privKey)
	if err != nil {
		return "", err
	}

	return password, nil
}

func sshParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to parse PEM block containing the key")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return priv, nil
	}
	// Try PKCS8
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	return nil, errors.New("failed to parse private key")
}

func decryptPassword(enc string, key *rsa.PrivateKey) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("base64 decode failed: %v", err)
	}
	plaintext, err := rsa.DecryptPKCS1v15(nil, key, ciphertext)
	if err != nil {
		return "", fmt.Errorf("rsa decrypt failed: %v", err)
	}
	return string(plaintext), nil
}

func startShellSession(instanceID, region string) error {
	if runtime.GOOS == "darwin" {
		script := fmt.Sprintf(`tell app "Terminal" to do script "aws ssm start-session --target %s --region %s"`, instanceID, region)
		cmd := exec.Command("osascript", "-e", script)
		return cmd.Run()
	}

	// For other OSes (Linux), run in the current terminal.
	// Note: This will cause interleaved output if multiple sessions are started.
	// A more advanced implementation could use `x-terminal-emulator` or other methods.
	cmd := exec.Command("aws", "ssm", "start-session", "--target", instanceID, "--region", region)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		// Enhanced error handling for SSM agent not connected
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "TargetNotConnected") {
				fmt.Println("❌ SSM session failed: The instance is not connected to SSM.")
				fmt.Println("👉 Please check if the SSM agent is running and properly configured on this instance.")
				return fmt.Errorf("SSM agent not connected on instance %s", instanceID)
			}
		}
		return fmt.Errorf("%v", err)
	}
	return nil
}
