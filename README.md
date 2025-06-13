# 📘 Tool Overview: `aws-ec2-connector` — Streamlined EC2 Access via SSM

## 🔍 Problem Statement

Connecting to EC2 instances via AWS Systems Manager (SSM) can often be time-consuming for team members—typically taking up to 10 minutes to locate the correct command, identify instance IDs, and authenticate.

## ✅ Solution

To address this inefficiency, we've developed a lightweight command-line utility called **`aws-ec2-connector`**. This tool simplifies and accelerates the process of connecting to EC2 instances by leveraging your AWS SSO session. It automates instance discovery and session initiation, enabling faster, error-free access.

---

## 🚀 Installation Instructions (macOS)

1. **Download the binary** and run the following commands in your terminal:

   ```sh
   chmod +x ~/Downloads/aws-ec2-connector
   sudo cp ~/Downloads/aws-ec2-connector /usr/local/bin
   sudo xattr -rd com.apple.quarantine /usr/local/bin/aws-ec2-connector
   ```

   > **Note:** Due to Apple Gatekeeper restrictions, the binary must be manually whitelisted using the `xattr` command since it’s not signed by an Apple-verified developer.

2. **Authenticate to your AWS account** using `go-aws-sso`:

   ```sh
   go-aws-sso
   ```

3. **Run the connector**:

   ```sh
   aws-ec2-connector --region <region_name>
   ```

   Replace `<region_name>` with your desired AWS region (e.g., `us-west-2`).

---

## 📌 Additional Notes

* The tool filters EC2 instances based on SSM availability.
* Ideal for environments with heavy use of SSO and SSM-based EC2 access.

---
