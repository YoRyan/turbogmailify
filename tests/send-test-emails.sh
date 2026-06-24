#!/bin/bash
#
# send-test-emails.sh <to-address>
#
# Send test emails to the given address so you can watch turbogmailify handle them.
#

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Usage: $0 <target-email>"
    exit 1
fi

if ! swaks --version > /dev/null; then
    echo "$0 requires 'swaks' to be installed.  See https://jetmore.org/john/code/swaks/"
    exit 1
fi

cd $(dirname $0)

TARGET="$1"
BODY="This is a turbogmailify test email pushed through swaks."

# Usage: <subject> [<swaks args> ...]
function swaks_send () {
    SUBJECT="$1"; shift
    SERVER=${SERVER:-localhost}

    echo "test \"$SUBJECT\" $*"
    # Use implicit, default "from" address which may not be right.
    swaks --silent --server $SERVER --to "$TARGET" --body "$BODY" --header "Subject: ${SUBJECT}" "$@"
}

# Test 1: basic email

swaks_send "turbogmailify test: basic email"

# Test 2: Attach a Windows executable (Gmail rejects email with such attachments).
# Lie about the attachment name because many services reject sending ".exe" files, based on the name (so secure!)

GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" hello.go

swaks_send "turbogmailify test: now with native hello" --attach-name hello.exe.hidden --attach @./hello.exe

# TODO: send a BCC'd email
# TODO: send a spammy email (or add spam headers?)

# Test N: send a final, normal email to be sure turbogmailify didn't get lost handling the above

swaks_send "turbogmailify final: basic email to make sure not side-tracked"


#eof
