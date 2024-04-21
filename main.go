// MIT License

// Copyright (c) 2024 Ryan Young

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
	flag.Parse()

	cfg := &config{}

	f, err := os.OpenFile(flag.Arg(0), os.O_RDWR, 0600)
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

	mail := getGmailService(*cfg, f)
	f.Close()

	// Spin up a goroutine for each IMAP connection.
	for _, imapCreds := range cfg.Imap {
		go func() {
			for {
				doSession(&imapCreds, mail)

				// Error'd out. Cool down and try again.
				time.Sleep(maxPollTime)
			}
		}()
	}

	// Put the main goroutine to sleep.
	log.Printf("Startup complete; waiting for mail")
	select {}
}

// Configuration file loaded from or saved to JSON.
type config struct {
	Imap    []imapCredentials
	Secrets interface{}
	Tokens  *oauth2.Token `json:",omitempty"`
}

// Save this configuration back to a JSON file.
func (c *config) writeTo(f *os.File) {
	if _, err := f.Seek(0, 0); err != nil {
		log.Fatalf("Unable to seek config file: %v", err)
	}

	if err := json.NewEncoder(f).Encode(c); err != nil {
		log.Fatalf("Unable to write back to config file: %v", err)
	}
}

// Information needed to connect to an IMAP server. Implicit TLS is mandatory.
type imapCredentials struct {
	Address  string
	Username string
	Password string
}

// Obtain access to the Gmail API, refreshing and saving access tokens if
// needed.
func getGmailService(cfg config, cfgFile *os.File) *gmail.Service {
	ctx := context.Background()

	// Need to submit the client secret as JSON bytes, leading to this silly
	// re-encode step.
	secrets, err := json.Marshal(cfg.Secrets)
	if err != nil {
		log.Fatalf("JSON re-encode error: %v", err)
	}

	oauth, err := google.ConfigFromJSON(secrets, gmail.GmailInsertScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret to oauth2 config: %v", err)
	}

	// Attempt to retrieve stored access and refresh tokens; otherwise request
	// them from Google.
	var tok *oauth2.Token
	if cfg.Tokens != nil {
		tok = cfg.Tokens
	} else {
		tok = getTokenFromWeb(oauth)

		cfg.Tokens = tok
		cfg.writeTo(cfgFile)
	}
	client := oauth.Client(ctx, tok)

	// Finally, create our Gmail client.
	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Gmail client: %v", err)
	}

	return srv
}

// Make a new connection to the IMAP server and retrieve and expunge messages.
func doSession(imap *imapCredentials, mail *gmail.Service) error {
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

	for {
		// Interrogate the inbox and retrieve and expunge everything inside.
		for {
			inbox, err := client.
				Select("INBOX", nil).
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
				"Importing message received by %s (size %.1fK)",
				imap.Username, float32(len(msg.contents))/1024)

			if err := msg.importToGmail(mail); err != nil {
				return err
			}

			if err := deleteMessage(client, msg.uid); err != nil {
				return err
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
			imap.SeqSetNum(1), &imap.FetchOptions{BodySection: entireMessage})
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
func (m *message) importToGmail(mail *gmail.Service) error {
	r, err := mail.Users.Messages.
		Import("me", &gmail.Message{LabelIds: []string{"INBOX", "UNREAD"}}).
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
		Wait(); err != nil {

		log.Printf("STORE error: %v", err)
		return err
	}

	if err := client.
		Expunge().
		Wait(); err != nil {

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
