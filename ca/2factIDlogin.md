# Two factor identification login

One can use Microsoft Authenticator for two-factor authentication (2FA) on your Linux machine. Microsoft Authenticator supports TOTP, which is the same standard used by Google Authenticator and other similar apps. Here’s how you can set it up:

### Step 1: Install the Necessary Packages
First, install the required packages on your Linux machine. Open a terminal and run the following commands:

```
sudo apt update
sudo apt install libpam-google-authenticator
```

### Step 2: Configure Google Authenticator
Run the Google Authenticator setup for your user account. This will generate a QR code and provide you with secret keys. You can then use the Microsoft Authenticator app on your phone to scan this QR code.

```
google-authenticator
```

During the setup, you will be prompted with several questions. Here are the recommended responses:
- **Do you want authentication tokens to be time-based? (Y/n)**: `Y`
- Your new secret key is: `[your-secret-key]`
- **Do you want me to update your "~/.google_authenticator" file? (Y/n)**: `Y`
- **Do you want to disallow multiple uses of the same authentication token? (y/N)**: `Y`
- **By default, tokens are good for 30 seconds. Do you want to increase the time skew to compensate for possible time discrepancies between the client and the server? (y/n)**: `N`
- **If the computer that you are logging into isn't hardened against brute-force login attempts, you can enable rate-limiting for the authentication module. Do you want to do so? (y/n)**: `Y`

### Step 3: Configure PAM to Use Google Authenticator
Edit the PAM configuration for SSH and login. You need to add the Google Authenticator PAM module to the appropriate files. Open the SSH PAM configuration file with:

```
sudo nano /etc/pam.d/sshd
```

Add the following line at the top:

```
auth required pam_google_authenticator.so
```

Next, open the login PAM configuration file:

```
sudo nano /etc/pam.d/common-auth
```

Add the same line:

```
auth required pam_google_authenticator.so
```

### Step 4: Configure SSH
Ensure that your SSH server is configured to require both password and 2FA. Open the SSH daemon configuration file:

```
sudo nano /etc/ssh/sshd_config
```

Ensure the following lines are set:

```
ChallengeResponseAuthentication yes
UsePAM yes
AuthenticationMethods password,keyboard-interactive
```

### Step 5: Restart the SSH Service
Restart the SSH service to apply the changes:

```
sudo systemctl restart ssh
```

### Step 6: Add Account to Microsoft Authenticator
1. Open the Microsoft Authenticator app on your phone.
2. Tap the "+" icon to add a new account.
3. Select "Other account (Google, Facebook, etc.)".
4. Scan the QR code displayed during the `google-authenticator` setup on your Linux machine.

### Step 7: Test Your Configuration
Try logging in via SSH from another device. You should be prompted for your password and then for a verification code from your Microsoft Authenticator app.

By following these steps, you can enable two-factor authentication on your Linux machine using the Microsoft Authenticator app.

#mbaigo