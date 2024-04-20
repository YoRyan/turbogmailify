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
	"flag"
	"log"
	"os"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const maxPollTime = 5 * time.Minute

func main() {
	var (
		imapAddress = flag.String(
			"address", "", "IMAP address to connect to (must use implicit TLS)")
		imapUsername = flag.String(
			"username", "", "IMAP username")
		imapPassword = flag.String(
			"password", "", "IAMP password")
		gmailSecrets = flag.String(
			"secrets", "credentials.json", "OAuth2 client secret file for Gmail")
		gmailTokens = flag.String(
			"tokens", "token.json", "OAuth2 access and refresh token storage for Gmail")
	)
	flag.Parse()

	mail := getGmailService(*gmailSecrets, *gmailTokens)

	for {
		doSession(
			imapCredentials{*imapAddress, *imapUsername, *imapPassword}, mail)

		// Error'd out. Cool down and try again.
		time.Sleep(maxPollTime)
	}
}

func getGmailService(secretsFile string, tokensFile string) *gmail.Service {
	ctx := context.Background()
	b, err := os.ReadFile(secretsFile)
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	// If modifying these scopes, delete your previously saved token.json.
	config, err := google.ConfigFromJSON(b, gmail.GmailInsertScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}
	client := getClient(config, tokensFile)

	srv, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Gmail client: %v", err)
	}

	return srv
}

type imapCredentials struct {
	tlsAddress string
	username   string
	password   string
}

// Make a new connection to the IMAP server and retrieve and expunge messages.
func doSession(imap imapCredentials, mail *gmail.Service) error {
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
		imap.tlsAddress, &imapclient.Options{UnilateralDataHandler: dataHandler})
	if err != nil {
		log.Printf("Error connecting to IMAP server: %v", err)
		return err
	}
	defer client.Close()

	// Provide credentials.
	if err := client.
		Login(imap.username, imap.password).
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
				"Importing email (size %.1fK)",
				float32(len(msg.contents))/1024)

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

type message struct {
	uid      imap.UID
	contents []byte
}

// Import this message to Gmail via media upload.
func (msg *message) importToGmail(mail *gmail.Service) error {
	r, err := mail.Users.Messages.
		Import("me", &gmail.Message{LabelIds: []string{"INBOX", "UNREAD"}}).
		InternalDateSource("dateHeader").
		NeverMarkSpam(false).
		ProcessForCalendar(true).
		Deleted(false).
		Media(
			bytes.NewReader(msg.contents),
			googleapi.ContentType("message/rfc822")).
		Do()
	if err != nil {
		log.Printf("Error uploading to Gmail: %v", err)
		return err
	}

	if r.HTTPStatusCode != 200 {
		log.Printf("Gmail returned status code: %v", r.HTTPStatusCode)
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
