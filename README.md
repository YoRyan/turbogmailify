# turbogmailify

Turbogmailify is a small Go program that imports mail from external accounts into your Gmail inbox at near real-time speed. It can be used as a faster, better replacement for Gmail's built-in POP3 importer, which was sunset in January 2026.  Compare to [Gomailify](https://www.gomailify.com/) and [Fetch2Gmail](https://github.com/threehappypenguins/fetch2gmail).

Turbogmailify:

- Connects via IMAP and takes advantage of the IDLE command to achieve nearly instantaneous detection of new emails.
- Expunges messages from the external server once they have been successfully uploaded to Gmail, exactly as what happened with a POP3 import.
- Doesn't use email forwarding, so messages won't be lost to Google's spam protection.
- Is small and easy to deploy on any home server or VPS.

Unfortunately, using the Gmail API to [import](https://developers.google.com/gmail/api/reference/rest/v1/users.messages/import) messages in this manner bypasses Gmail's automatic spam detection and message filtering features. But otherwise, this is pretty close the holy grail of Gmail integration with non-Gmail accounts: instant delivery, *without* resorting to email forwarding and the unreliability that entails.

For more background information about why I created this program, you can check out my relevant blog [post](https://youngryan.com/2024/check-emails-from-gmail-briskly-go-getmail/).

## Usage

### Google Cloud setup

The setup process is very similar to that of [gogcli](https://github.com/steipete/gogcli?tab=readme-ov-file#quick-start), the Google CLI that has become so fashionable among OpenClaw users.

1. [Create](https://console.cloud.google.com/projectcreate) a project for your Turbogmailify instance in Google Cloud Console.
2. [Enable](https://console.cloud.google.com/apis/api/gmail.googleapis.com) the Gmail API for this project.
3. [Configure](https://console.cloud.google.com/auth/branding) your project's OAuth branding. Personal Google accounts can only create "External" projects, but this is okay.
4. Your project will be initialized in the "Testing" state. You'll have to [add](https://console.cloud.google.com/auth/audience) yourself (or whichever Google account you want to forward mail to) as a test user.
5. [Create](https://console.cloud.google.com/auth/clients) a new client for your project. Choose the "Desktop" type.
6. Download the JSON secrets file that Google provides for your client.

### Write the configuration file

Turbogmailify accepts a single configuration file in JSON format. (It's handy to keep a JSON [validator](https://jsonlint.com/) nearby when writing your file.)

```json
{
  "Imap": [
    {
      "Address": "imap.purelymail.com:993",
      "Username": "AzureDiamond@example.com",
      "Password": "hunter2",
      "Folders": {
        "INBOX": [
          "INBOX"
        ],
        "Junk": [
          "SPAM"
        ]
      },
      "IdleFolder": "INBOX"
    }
  ],
  "Secrets": {
    "installed": {
      "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
      "auth_uri": "https://accounts.google.com/o/oauth2/auth",
      "client_id": "xxx.apps.googleusercontent.com",
      "client_secret": "GOCSPX-xxx",
      "project_id": "turbogmailify",
      "redirect_uris": [
        "http://localhost"
      ],
      "token_uri": "https://oauth2.googleapis.com/token"
    }
  }
}
```

The `Imap` section specifies one or more external IMAP servers to connect to.

The `Folders` sub-section maps IMAP folders to Gmail labels. Please note that labels must be specified using their unique identifiers, not their human-readable names. For built-in "system" labels, these values are identical, but "user" labels have randomly generated identifiers. You can obtain these identifiers by running [this](https://gist.github.com/YoRyan/4f9d28531d2b2eb9014dcb2c627aa10b) Google App Script against [your account](https://script.google.com/home). Specifying a mapping is optional; if omitted, turbogmailify uses the INBOX and Junk mapping depicted in this sample.

The IMAP protocol allows a client to use the IDLE command to receive instantaneous notifications of incoming mail for a single folder. The `IdleFolder` sub-key specifies which folder Turbogmailify will watch. If omitted, the default is the INBOX folder. (Regardless of these notifications, Turbogmailify checks all configured folders at least as often as every 5 minutes.)

The `Secrets` section should contain all the JSON data from the `credentials.json` file that Google provides you in step #5 of the Google Cloud setup section.

### Obtain access and refresh tokens

You need to complete the OAuth2 flow with your Google account so that Turbogmailify can access the Gmail API. In a terminal, run:

```
$ go build
$ ./turbogmailify myconfig.file -auth
```

(or)

```
$ docker run -it --rm -v ./myconfig.file:/turbogmailify.conf ghcr.io/yoryan/turbogmailify -auth /turbogmailify.conf
```

After completing this flow, you'll receive access and refresh tokens in the form of JSON data. Incorporate this data into your configuration file as the `Tokens` section:

```json
{
  "Imap": [
    ...
  ],
  "Secrets": {
    "installed": {
      "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
      "auth_uri": "https://accounts.google.com/o/oauth2/auth",
      "client_id": "xxx.apps.googleusercontent.com",
      "client_secret": "GOCSPX-xxx",
      "project_id": "turbogmailify",
      "redirect_uris": [
        "http://localhost"
      ],
      "token_uri": "https://oauth2.googleapis.com/token"
    }
  },
  "Tokens": {
    "access_token": "xxx",
    "token_type": "Bearer",
    "refresh_token": "xxx",
    "expiry": "2026-04-04T09:02:00.907727817Z"
  } 
}
```

### Regular operations

With your configuration file fully populated with IMAP and both kinds of Google credentials, you can run Turbogmailify without the `-auth` flag, and the program will run indefinitely and start forwarding mail.

A suggested systemd service is as follows:

```toml
[Unit]
Description=Turbogmailify IMAP to Gmail

[Service]
Type=exec
ExecStart=/usr/local/bin/turbogmailify /etc/turbogmailify.conf
Restart=always
RestartSec=10s

[Install]
WantedBy=multi-user.target
```

A suggested Docker Compose service is as follows:

```yaml
services:
  turbogmailify:
    image: ghcr.io/yoryan/turbogmailify
    container_name: turbogmailify
    restart: unless-stopped
    configs:
      - source: turbogmailify
        target: /turbogmailify.conf
configs:
  turbogmailify:
    content: |
      {
        "Imap": [ ... ]
        "Secrets": { ... }
        "Tokens": { ... }
      }
```
