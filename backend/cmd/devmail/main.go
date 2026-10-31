// Command devmail runs local IMAPS and SMTPS servers (in-memory) populated
// with generated messages. It is a development and benchmarking tool and is
// not included in the production image.
//
//	go run ./cmd/devmail -messages 10000 -accounts 2 -ca /tmp/devmail-ca.pem
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"

	"lightmail/internal/testmail"
)

var (
	firstNames = []string{"Alice", "Bruno", "Chloé", "David", "Émilie", "François", "Gaëlle", "Hugo", "Inès", "Julien", "Karim", "Léa", "Mathis", "Nora", "Oscar", "Paul"}
	topics     = []string{"Facture", "Réunion", "Projet", "Rapport mensuel", "Livraison", "Invitation", "Contrat", "Relance", "Budget 2026", "Compte rendu", "Devis", "Planning"}
	words      = strings.Fields("lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua merci pour votre retour concernant le dossier nous restons disponibles cordialement bonne journée")
)

func sentence(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(words[r.IntN(len(words))])
	}
	return b.String()
}

func main() {
	imapAddr := flag.String("imap", "127.0.0.1:1993", "IMAPS listen address")
	smtpAddr := flag.String("smtp", "127.0.0.1:1465", "SMTPS listen address")
	n := flag.Int("messages", 2000, "messages per account in INBOX")
	accounts := flag.Int("accounts", 1, "number of accounts (dev1@devmail.test, dev2@...)")
	caPath := flag.String("ca", "devmail-ca.pem", "where to write the CA certificate")
	password := flag.String("password", "devmail-pass-1", "password of every account")
	live := flag.Duration("live", 0, "deliver a new message to dev1 at this interval (0 = off)")
	hostnames := flag.String("hostnames", "devmail", "extra certificate host names (comma separated)")
	flag.Parse()

	cert, err := testmail.NewCert(strings.Split(*hostnames, ",")...)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caPath, []byte(cert.CAPEM), 0o644); err != nil {
		log.Fatal(err)
	}
	is, err := testmail.StartIMAP(cert, *imapAddr)
	if err != nil {
		log.Fatal(err)
	}
	users := map[string]string{}
	r := rand.New(rand.NewPCG(42, 7))
	start := time.Now()
	for a := 1; a <= *accounts; a++ {
		user := fmt.Sprintf("dev%d@devmail.test", a)
		users[user] = *password
		is.AddUser(user, *password, "INBOX", "Sent", "Drafts", "Trash", "Archive", "Projets", "Projets/2026")
		base := time.Now().Add(-time.Duration(*n) * 37 * time.Minute)
		for i := 0; i < *n; i++ {
			from := firstNames[r.IntN(len(firstNames))]
			addr := strings.ToLower(strings.NewReplacer("é", "e", "ë", "e", "è", "e").Replace(from)) + "@exemple.fr"
			subject := fmt.Sprintf("%s n°%d — %s", topics[r.IntN(len(topics))], i, sentence(r, 3))
			date := base.Add(time.Duration(i) * 37 * time.Minute)
			var raw []byte
			switch {
			case i%17 == 0:
				raw = testmail.MessageWithAttachment(fmt.Sprintf("%s <%s>", from, addr), user, subject,
					"<p>Bonjour,</p><p>"+sentence(r, 40)+"</p><p><img src=\"https://tracker.example/pixel.gif\"></p>", fmt.Sprintf("document-%d.pdf", i),
					[]byte("%PDF-1.4 "+sentence(r, 200)), date)
			default:
				extra := []string{}
				if i%9 == 0 && i > 0 {
					extra = append(extra, fmt.Sprintf("In-Reply-To: <%d.%s>", base.Add(time.Duration(i-1)*37*time.Minute).UnixNano(), strings.ReplaceAll(addr, "@", ".")+"@test"))
				}
				if i%50 == 0 {
					extra = append(extra, "X-Priority: 1")
				}
				raw = testmail.Message(fmt.Sprintf("%s <%s>", from, addr), user, subject, "Bonjour,\r\n\r\n"+sentence(r, 30+r.IntN(200))+"\r\n\r\n"+from, date, extra...)
			}
			var flags []imap.Flag
			if i < *n-30 && r.IntN(4) != 0 {
				flags = append(flags, imap.FlagSeen)
			}
			if r.IntN(40) == 0 {
				flags = append(flags, imap.FlagFlagged)
			}
			if err := is.Deliver(user, "INBOX", raw, flags...); err != nil {
				log.Fatal(err)
			}
		}
	}
	ss, err := testmail.StartSMTPAddr(cert, users, *smtpAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("devmail ready in %s: IMAPS %s, SMTPS %s:%d, %d account(s) x %d messages, password %q, CA written to %s",
		time.Since(start).Round(time.Millisecond), *imapAddr, ss.Host, ss.Port, *accounts, *n, *password, *caPath)

	if *live > 0 {
		go func() {
			t := time.NewTicker(*live)
			for i := 0; ; i++ {
				<-t.C
				raw := testmail.Message("Live <live@exemple.fr>", "dev1@devmail.test", fmt.Sprintf("Nouveau message %d", i), sentence(r, 40), time.Now())
				_ = is.Deliver("dev1@devmail.test", "INBOX", raw)
			}
		}()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("received %d messages over SMTP", len(ss.Received()))
}
