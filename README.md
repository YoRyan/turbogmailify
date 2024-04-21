# turbogmailify

Turbogmailify is a small Go program that imports mail from external accounts into your Gmail inbox at near real-time speed. It can be used as an alternative to, or can work alongside of, Gmail's built-in POP3 importer, which is notorious for taking long periods between refreshes.

Turbogmailify:

- Connects via IMAP and takes advantage of the IDLE command to achieve nearly instantaneous detection of new emails.
- Expunges messages from the external server once they have been successfully uploaded to Gmail, exactly as what happens with a POP3 import.
- Doesn't use email forwarding, so messages won't be lost to Google's spam protection.
- Is small and easy to deploy on any home server or VPS.

This is the holy grail of Gmail integration with non-Gmail accounts: instant delivery, *without* resorting to email forwarding and the unreliability that entails.