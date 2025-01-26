# turbogmailify

Turbogmailify is a small Go program that imports mail from external accounts into your Gmail inbox at near real-time speed. It can be used as an alternative to, or can work alongside of, Gmail's built-in POP3 importer, which is notorious for taking long periods between refreshes.

Turbogmailify:

- Connects via IMAP and takes advantage of the IDLE command to achieve nearly instantaneous detection of new emails.
- Expunges messages from the external server once they have been successfully uploaded to Gmail, exactly as what happens with a POP3 import.
- Doesn't use email forwarding, so messages won't be lost to Google's spam protection.
- Is small and easy to deploy on any home server or VPS.

Unfortunately, using the Gmail API to [import](https://developers.google.com/gmail/api/reference/rest/v1/users.messages/import) messages in this manner bypasses Gmail's automatic spam detection and message filtering features. But otherwise, this is pretty close the holy grail of Gmail integration with non-Gmail accounts: instant delivery, *without* resorting to email forwarding and the unreliability that entails.

For more background information about why I created this program, you can check out my relevant blog [post](https://youngryan.com/2024/check-emails-from-gmail-briskly-go-getmail/).

## Configuration

First things first, you'll need to [create](https://developers.google.com/gmail/api/quickstart/go) a personal Google Cloud project and enable access to the Gmail API.

The lone argument supplied to turbogmailify is a path to its JSON configuration file.

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
      }
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

The `Folders` sub-section maps IMAP folders to Gmail labels. Specifying a mapping is optional; if omitted, turbogmailify uses the INBOX and Junk mapping depicted in this sample.

The `Secrets` section should contain all the JSON data from the `credentials.json` file that Google provides you when you create a client ID for your Cloud project, following the steps in the Go quickstart [tutorial](https://developers.google.com/gmail/api/quickstart/go).

However you choose to run turbogmailify, the configuration file *must* be read-writeable by the program, because it writes back newly obtained access and refresh tokens. When you run turbogmailify against your Google account for the first time, it will print an OAuth consent URL to the console. You'll have to authorize your Google account with this consent screen and then paste the authorization code back into the console.