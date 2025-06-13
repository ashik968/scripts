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
	"strings"
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
	selected  *InstanceInfo
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
		case "enter":
			if m.cursor >= 0 && m.cursor < len(m.instances) {
				m.selected = &m.instances[m.cursor]
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m instanceSelectorModel) View() string {
	s := "📋 Select an EC2 Instance (Use ↑/↓ to navigate, Enter to select, q to quit):\n\n"
	for i, inst := range m.instances {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}
		platform := "Linux"
		if inst.Platform != "" {
			platform = inst.Platform
		}
		s += fmt.Sprintf("%s [%d] %-20s | %s | %s | %s\n", cursor, i+1, inst.Name, inst.ID, inst.PrivateIP, platform)
	}
	return s
}

func selectInstance(instances []InstanceInfo) (*InstanceInfo, error) {
	m := instanceSelectorModel{instances: instances}
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("TUI failed: %v", err)
	}
	m = finalModel.(instanceSelectorModel)
	if m.selected == nil {
		return nil, fmt.Errorf("no instance selected")
	}
	return m.selected, nil
}

func main() {
	region := flag.String("region", "ap-northeast-1", "AWS region (default: Tokyo)")
	flag.Parse()

	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(*region))
	if err != nil {
		log.Fatalf("❌ Unable to load AWS config: %v", err)
	}

	instances, err := listRunningInstances(cfg)
	if err != nil {
		log.Fatalf("❌ Failed to list instances: %v", err)
	}

	if len(instances) == 0 {
		log.Fatal("🚫 No running EC2 instances found in this region.")
	}

	selected, err := selectInstance(instances)
	if err != nil {
		log.Fatalf("❌ Failed to select instance: %v", err)
	}

	boldPrint("✅ Selected instance: " + selected.Name + " (" + selected.ID + ")")

	if selected.Platform == "windows" {
		fmt.Print("🔑 Enter full path to your Windows EC2 PEM private key: ")
		keyPath := readLine()

		password, err := getWindowsPassword(cfg, selected.ID, keyPath)
		if err != nil {
			log.Fatalf("❌ Failed to get Windows password: %v", err)
		}

		_ = copyToClipboard(password)
		yellowBoldPrint("🔐 Windows Administrator password (copied to clipboard): " + password)

		err = startPortForward(selected.ID, *region)

	} else {
		err = startShellSession(selected.ID, *region)
	}

	if err != nil {
		// Only print the error if it's not the SSM agent not connected message
		if !strings.Contains(err.Error(), "SSM agent not connected") {
			log.Fatalf("❌ SSM session failed: %v", err)
		}
		// Otherwise, just exit (the user already saw the friendly message)
		os.Exit(1)
	}
}

func listRunningInstances(cfg aws.Config) ([]InstanceInfo, error) {
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
	out, err := ec2Client.DescribeInstances(context.TODO(), &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
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

func startPortForward(instanceID, region string) error {
	port := "9000"
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
