// MIT License

// Copyright (c) 2024-2026 Ryan Young

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const maxPollTime = 5 * time.Minute

func main() {
	doAuth := flag.Bool("auth", false, "Request access and refresh tokens from Google instead of processing mail.")
	flag.Parse()

	if flag.NArg() < 1 {
		log.Fatalf("Did not pass path to configuration file.")
	}

	cfg := &config{}

	f, err := os.OpenFile(flag.Arg(0), os.O_RDONLY, 0600)
	if err != nil {
		log.Fatalf("Failed to open config file: %v", err)
	}

	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		log.Fatalf("Failed to parse config file: %v", err)
	}

	if len(cfg.Imap) <= 0 {
		log.Fatalf("Failed to parse config file: " +
			"IMAP credentials section is empty or not a JSON list")
	}

	if cfg.Secrets == nil {
		log.Fatalf("Failed to parse config file: " +
			"Google credentials ('Secrets') are missing")
	}

	if *doAuth {
		doRequestAuth(cfg)
	} else {
		doForwarding(cfg)
	}
}

// Configuration file loaded from or saved to JSON.
type config struct {
	Imap    []imapCredentials
	Secrets interface{}
	Tokens  *oauth2.Token `json:",omitempty"`
}

func (c *config) getOAuthConfig() (oauth *oauth2.Config, err error) {
	// Need to submit the client secret as JSON bytes, leading to this silly
	// re-encode step.
	secrets, err := json.Marshal(c.Secrets)
	if err != nil {
		return
	}

	oauth, err = google.ConfigFromJSON(secrets, gmail.GmailInsertScope)
	return
}

// Information needed to connect to an IMAP server. Implicit TLS is mandatory.
type imapCredentials struct {
	Address    string
	Username   string
	Password   string
	Folders    map[string][]string
	IdleFolder string // folder to IDLE on; defaults to "INBOX" if empty
}

// Run in request tokens mode.
func doRequestAuth(c *config) {
	oauth, err := c.getOAuthConfig()
	if err != nil {
		log.Fatalf("Unable to create Google OAuth2 client: %v", err)
	}

	authURL := oauth.AuthCodeURL("", oauth2.AccessTypeOffline)
	fmt.Printf("Navigate to the following URL in your browser:\n%v\n", authURL)

	var redirectURL string
	fmt.Printf("\nOnce you've authorized the request, your browser will redirect to a URL that will not load. Paste the entire URL here:\n")
	if _, err := fmt.Scan(&redirectURL); err != nil {
		log.Fatalf("Unable to read redirected URL: %v", err)
	}

	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		log.Fatalf("Unable to parse redirected URL: %v", err)
	}

	tokens, err := oauth.Exchange(context.Background(), parsedURL.Query().Get("code"))
	if err != nil {
		log.Fatalf("Unable to retrieve tokens from Google: %v", err)
	}

	fmt.Printf("\nYour 'Tokens' section is as follows:\n")
	if err := json.NewEncoder(os.Stdout).Encode(tokens); err != nil {
		log.Fatalf("Error converting tokens to JSON: %v", err)
	}
}

// Run in mail forwarding mode.
func doForwarding(c *config) {
	if c.Tokens == nil {
		log.Fatalf("Authorization tokens ('Tokens') are missing. Obtain them with -auth mode.")
	}

	oauth, err := c.getOAuthConfig()
	if err != nil {
		log.Fatalf("Unable to create Google OAuth2 client: %v", err)
	}

	ctx := context.Background()
	mail, err := gmail.NewService(ctx, option.WithHTTPClient(oauth.Client(ctx, c.Tokens)))
	if err != nil {
		log.Fatalf("Unable to create Gmail client: %v", err)
	}

	// Spin up a goroutine for each IMAP connection.
	for _, imapCreds := range c.Imap {
		go func() {
			for {
				doImapSession(&imapCreds, mail)

				// Error'd out. Cool down and try again.
				time.Sleep(maxPollTime)
			}
		}()
	}

	// Put the main goroutine to sleep.
	log.Printf("Startup complete; waiting for mail")
	select {}
}

// Make a new connection to the IMAP server and retrieve and expunge messages.
func doImapSession(imap *imapCredentials, mail *gmail.Service) error {
	// Make a handler and channel to receive mailbox status updates.
	var (
		mailboxUpdate = make(chan *imapclient.UnilateralDataMailbox)
		dataHandler   = &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				select {
				case mailboxUpdate <- data:
				default:
				}
			}}
	)

	// Open the connection.
	client, err := imapclient.DialTLS(
		imap.Address, &imapclient.Options{UnilateralDataHandler: dataHandler})
	if err != nil {
		log.Printf("Error connecting to IMAP server: %v", err)
		return err
	}
	defer client.Close()

	// Provide credentials.
	if err := client.
		Login(imap.Username, imap.Password).
		Wait(); err != nil {

		log.Printf("LOGIN error: %v", err)
		return err
	}

	var folders map[string][]string
	if len(imap.Folders) > 0 {
		folders = imap.Folders
	} else {
		folders = map[string][]string{
			"INBOX": {"INBOX"},
			"Junk":  {"SPAM"},
		}
	}

	// Determine which folder to IDLE on. It must always be the last folder
	// selected before IDLE — an extra SELECT between the drain loop and IDLE
	// causes some servers (e.g. free.fr) to stop sending push notifications.
	// We achieve this by draining all other folders first and the idle folder
	// last, so no additional SELECT is needed after the loop.
	var idleFolder string
	if imap.IdleFolder == "" {
		idleFolder = "INBOX"
	} else {
		idleFolder = imap.IdleFolder
	}
	if _, idleFolderExists := folders[idleFolder]; !idleFolderExists {
		// Well, we need to idle on some folder, so just pick one...
		for folder := range folders {
			idleFolder = folder
			break
		}
	}

	orderedFolders := make([]string, 0, len(folders))
	for folder := range folders {
		if folder != idleFolder {
			orderedFolders = append(orderedFolders, folder)
		}
	}
	orderedFolders = append(orderedFolders, idleFolder)

	for {
		// Interrogate each folder and retrieve and expunge everything inside.
		// Idle folder is always last so it remains selected when we enter IDLE.
		for _, folder := range orderedFolders {
			labels := folders[folder]
			for {
				inbox, err := client.
					Select(folder, nil).
					Wait()
				if err != nil {
					log.Printf("SELECT error: %v", err)
					return err
				}

				if inbox.NumMessages <= 0 {
					break
				}

				msg, err := fetchFirstMessage(client)
				if err != nil {
					return err
				}
				if msg == nil {
					break
				}

				log.Printf(
					"Importing message received by %s (uid %d, size %.1fK, folder %s)",
					imap.Username, msg.uid, float32(len(msg.contents))/1024, folder)

				if err := msg.importToGmail(mail, labels...); err != nil {
					return err
				}

				if err := deleteMessage(client, msg.uid); err != nil {
					return err
				}
			}
		}

		// Go back to sleep until the next mailbox update.
		if err := doIdle(client, mailboxUpdate, maxPollTime); err != nil {
			return err
		}
	}
}

// Retrieve inbox message sequence number 1.
func fetchFirstMessage(client *imapclient.Client) (*message, error) {
	var (
		// Set an empty body section to request the raw contents of the entire
		// message.
		entireMessage = []*imap.FetchItemBodySection{{}}
		fetch         = client.Fetch(
			imap.SeqSetNum(1), &imap.FetchOptions{BodySection: entireMessage, UID: true})
	)
	defer fetch.Close()

	messages, err := fetch.Collect()
	if err != nil {
		log.Printf("FETCH error: %v", err)
		return nil, err
	}

	if len(messages) <= 0 {
		return nil, nil
	}

	var (
		msg  = messages[0]
		data []byte
	)
	for _, sectionData := range msg.BodySection {
		data = append(data, sectionData...)
	}
	return &message{
		uid:      msg.UID,
		contents: data}, nil
}

// An email fetched from an IMAP mailbox.
type message struct {
	uid      imap.UID
	contents []byte
}

// Import this message to Gmail via media upload.
func (m *message) importToGmail(mail *gmail.Service, labels ...string) error {
	r, err := mail.Users.Messages.
		Import("me", &gmail.Message{LabelIds: append(labels, "UNREAD")}).
		InternalDateSource("dateHeader").
		NeverMarkSpam(false).
		ProcessForCalendar(true).
		Deleted(false).
		Media(
			bytes.NewReader(m.contents),
			googleapi.ContentType("message/rfc822")).
		Do()
	if err != nil {
		log.Printf("Error uploading to Gmail: %v", err)
		return err
	}

	if r.HTTPStatusCode != 200 {
		err := fmt.Errorf("gmail returned status code: %v", r.HTTPStatusCode)
		log.Printf("%v", err)
		return err
	}

	return nil
}

// Expunge a message from the inbox by UID.
func deleteMessage(client *imapclient.Client, uid imap.UID) error {
	var (
		setNum     = imap.UIDSetNum(uid)
		addDeleted = &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted}}
	)
	if err := client.
		Store(setNum, addDeleted, nil).
		Close(); err != nil {

		log.Printf("STORE error: %v", err)
		return err
	}

	if err := client.
		Expunge().
		Close(); err != nil {

		log.Printf("EXPUNGE error: %v", err)
		return err
	}

	return nil
}

// Block until the next mailbox status update, or until the deadline has
// elapsed.
func doIdle(
	client *imapclient.Client,
	mailboxUpdate chan *imapclient.UnilateralDataMailbox,
	deadline time.Duration,
) error {
	idle, err := client.Idle()
	if err != nil {
		log.Printf("IDLE error: %v", err)
		return err
	}

	timer := time.NewTimer(deadline)
	select {
	case <-mailboxUpdate:
		timer.Stop()
	case <-timer.C:
	}

	if err := idle.Close(); err != nil {
		log.Printf("IDLE error: %v", err)
		return err
	}

	if err := idle.Wait(); err != nil {
		log.Printf("IDLE error: %v", err)
		return err
	}

	return nil
}
