# turbogmailify

Turbogmailify is a self-hosted replacement for Gmail's built-in POP3 importer, which was sunset in January 2026. It collects emails from IMAP servers and imports them using Google's official [Gmail API](https://developers.google.com/workspace/gmail/api/guides). In addition to still being available, Turbogmailify is also much faster and more flexible compared to Google's former POP importer.

### Features and limitations

✅ Speed: Turbogmailify polls your IMAP inbox every few minutes, as opposed to Google's POP importer, which was notorious for taking up to an hour between refreshes. On many IMAP servers, Turbogmailify can take advantage of the IDLE command to achieve instantaneous detection of new emails.

✅ Flexibility: You can configure Turbogmailify to check multiple IMAP accounts and apply custom labels to the emails it imports.

✅ Reliability: The final import step, expunging the original message from the IMAP inbox, is executed by Turbogmailify if and only if the message has been successfully imported into Gmail. Turbogmailify checks the Junk folder, too, so you won't lose any important messages that have been marked false positives.

⚒️ Classification: Gmail's label classification seems to work only sporadically on imported emails, and custom Gmail filters won't run at all. Consider using your IMAP inbox's filters, if available, as an alternative.

⚒️ Spam Filtering: Gmail's spam filter doesn't run on imported emails. But by applying the Spam label to emails that have been placed in the IMAP Junk folder, Turbogmailify can piggyback on the spam filter equipped by your IMAP inbox.

### FAQ

#### Why not just forward email to my Gmail inbox?

If you just forward your emails over the email network, Google has a habit of silently dropping messages that look like spam, including false positives. Such "spam" will also count against your domain's trustworthiness score, making it all the more harder for subsequent messages to get through. In short, email forwarding, the obvious solution, leads inevitably to a downward spiral of unreliable delivery.

#### What about paid services? [Gomailify](https://www.gomailify.com/)? [Postdirect](https://postdirect.net/mailbox)?

Paying somebody to provide this service for you is certainly easier than deploying a solution like Turbogmailify. But if you have the skills to run self-hosted software, why not cut out the middleman? This is your private email we're talking about, after all—some of your most sensitive personal communications.

If running cost is a concern, you don't necessarily need to pay for a VPS just to run Turbogmailify. Your IMAP inbox is already doing the heavy lifting of maintaining multiple 9's of availability to receive email, so a forwarder like Turbogmailify just needs be available *most* of the time. A home server with a residential Internet connection will do just fine.

#### What about other self-hosted solutions? [Fetch2Gmail](https://github.com/threehappypenguins/fetch2gmail)? [InboxBridge](https://github.com/tdferreira/inboxbridge)?

Brevity is the soul of wit: Turbogmailify accomplishes everything it needs to do in less than 500 lines of easily auditable Go code (reputable imports excepted). There are no web UI's, SQL databases, or giant LLM-written commits here. As of April 2026, Turbogmailify also includes key features that Fetch2Gmail does not, such as support for IMAP IDLE, support for IMAP folders, and support for Gmail labels.

Turbogmailify's development history dates back to [2024](https://youngryan.com/2024/check-emails-from-gmail-briskly-go-getmail/) and has served as the author's principal way to import email since then. Knock on wood, it has yet to lose a single message.

#### What about IMAP to IMAP syncing? [imapsync](https://imapsync.lamiral.info/)? [Magpie](https://github.com/FynleyMsg/Magpie)?

Turbogmailify is engineered specifically for Gmail and supports Gmail's OAuth flow and labels system. Interacting with a Gmail inbox as if it were just an ordinary IMAP server will always result in some level of friction.

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
