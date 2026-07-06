package llm

// Live eval for FilterOrderMessages (Tier 2): runs the REAL model against an
// inbox fixture reproduced VERBATIM from the Phase 1 smoke test — the real
// Graph conversation ids, the real \r\n-riddled bodyPreviews, and the full
// volume of near-duplicate account-security junk that fooled the filter three
// times in production. Skipped unless ANTHROPIC_API_KEY is set, so the default
// suite stays offline. Runs multiple trials because a boundary case that
// passes once can still fail in production.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
)

const filterTrials = 3

// Real junk conversation ids captured from the production payload log.
const (
	convMSApps1    = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAGfwwrX8ldBEgMVN35WgVzs=" // kept by the filter in prod — the repeat offender
	convMSApps2    = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAEDxWFOWhNBJtRK8vSjEjNo="
	convMSApps3    = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAC5SjkcrY5lJgp_57F-k1oU="
	convMSSignIn   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQACQR-cJsMKNLinwIPuYk05U="
	convMaestro1   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQABrFk5Ly6j9HvBcuu3Wgeu4="
	convMaestro2   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAMBlBL5sUCZPhelOM-6_-0g="
	convMaestro3   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAMwdevaUQqZPvjDZR8iW1E4="
	convMaestro4   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQADO5VCzdcq9Nn0laremNeX8="
	convM365       = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQABJZt0ilU4RNjD-g42VTrCw="
	convCongrats   = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAOiMcVezVwFIjXdjYZUZR6Q="
	convNordic8832 = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAMNQvr_u9RlHmwVshxf1uno="
	convBrightline = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAK1qA9dWValInQgEGG-OlfE="
	convDelmar     = "AQQkADAwATM0MDAAMS03MmJlLTBkNWYtMDACLTAwCgAQAJx3Vm2kQdBLpTn81wKvGh0="
)

type fixtureMsg struct {
	conv, from, fromName, subject, preview string
}

func fixtureInbox() []fixtureMsg {
	return []fixtureMsg{
		{convMSApps1, "account-security-noreply@accountprotection.microsoft.com", "Microsoft account team",
			"New app(s) connected to your Microsoft account",
			"Microsoft account\r\nNew app(s) have access to your data\r\nGraph Explorer connected to the Microsoft account ca**1@outlook.com.\r\nIf you didn't grant this access, please remove the app(s) from your account.\r\nManage your apps\r\nYou can also opt out or change wh"},
		{convMSApps2, "account-security-noreply@accountprotection.microsoft.com", "Microsoft account team",
			"New app(s) connected to your Microsoft account",
			"Microsoft account\r\nNew app(s) have access to your data\r\nGraph Explorer connected to the Microsoft account ca**1@outlook.com.\r\nIf you didn't grant this access, please remove the app(s) from your account.\r\nManage your apps\r\nYou can also opt out or change wh"},
		{convMaestro1, "email@maestrolabs.com", "Adrian - MailMaestro",
			"Julian, confirm and decline meetings faster ⚡️",
			"No more back-and-forth emails.\r\nEmail didn't load right? Click here to view on browser.\r\n\r\n\r\nHi Julian,\r\n\r\nMailMaestro helps you accept and reject meeting requests effortlessly with Meeting request.\r\n\r\nWhen you keep MailMaestro open, it automatically scan"},
		{convMaestro2, "email@maestrolabs.com", "Adrian - MailMaestro",
			"Julian, set your AI personality",
			"Avoid sounding like a robot. 🤖\r\nEmail didn't load right? Click here to view on browser.\r\n\r\n\r\nHi Julian,\r\n\r\nMake your emails sound just like you with AI Personality 🪄\r\n\r\nAI Personality allows you to provide context about yourself to MailMaestro, enabling"},
		{convMaestro3, "email@maestrolabs.com", "Adrian - MailMaestro",
			"Julian, summarize emails and attachments",
			"You’ll never have to waste time in emails again 😉\r\nEmail didn't load right? Click here to view on browser.\r\n\r\n\r\nHi Julian,\r\n\r\nStop spending too much time going through emails, and start saving time using MailMaestro!\r\n\r\nAutomatically summarize every emai"},
		{convMaestro4, "email@maestrolabs.com", "Adrian - MailMaestro",
			"Julian, reply with AI",
			"Never worry about wasting time drafting replies.\r\nEmail didn't load right? Click here to view on browser.\r\n\r\n\r\nHi Julian,\r\n\r\nReply to emails much faster with MailMaestro!\r\n\r\nJust open an email you wish to reply to, launch MailMaestro, and hit the \"Reply\" "},
		{convM365, "m365dev@microsoft.com", "Microsoft 365 Developer Program",
			"Welcome to the Microsoft 365 Developer Program - please take the next step!",
			"Microsoft 365 Developer Program | Access your profile\r\n\r\nMicrosoft 365 Developer Program\r\n\r\nWelcome to the Microsoft 365 Developer Program\r\nCongratulations! You have successfully joined the Microsoft 365 Developer Program. If you're a qualified member, yo"},
		{convCongrats, "cabrerajulian401@outlook.com", "Julian Cabrera",
			"Re: Congratulations, Julian!",
			"________________________________\r\nFrom: Adrian - MailMaestro <email@maestrolabs.com>\r\nSent: Saturday, June 27, 2026 6:43 PM\r\nTo: cabrerajulian401@outlook.com <cabrerajulian401@outlook.com>\r\nSubject: Congratulations, Julian!\r\n\r\nImprove emails with AI\r\nEm"},
		{convCongrats, "email@maestrolabs.com", "Adrian - MailMaestro",
			"Congratulations, Julian!",
			"Improve emails with AI\r\nEmail didn't load right? Click here to view on browser.\r\n\r\n\r\nHi Julian,\r\n\r\nWelcome to MailMaestro!\r\n\r\nReady to start saving time on emails? Let’s get you started with ✨ Improve drafts ✨\r\n\r\nMailMaestro lets you improve your emails b"},
		{convMSApps3, "account-security-noreply@accountprotection.microsoft.com", "Microsoft account team",
			"New app(s) connected to your Microsoft account",
			"Microsoft account\r\nNew app(s) have access to your data\r\nMailMaestro - ChatGPT for Outlook connected to the Microsoft account ca**1@outlook.com.\r\nIf you didn't grant this access, please remove the app(s) from your account.\r\nManage your apps\r\nYou can also o"},
		{convMSSignIn, "account-security-noreply@accountprotection.microsoft.com", "Microsoft account team",
			"Microsoft account unusual sign-in activity",
			"Microsoft account\r\nUnusual sign-in activity\r\nWe detected something unusual about a recent sign-in to the Microsoft account ca**1@outlook.com.\r\nSign-in details\r\nCountry/region: United States\r\nIP address: 150.252.240.2\r\nDate: 6/25/2026 10:58 PM (GMT)\r\nPlatf"},
		// Vendor threads — the conversations the filter must keep.
		{convNordic8832, "priya.shah.nordic@gmail.com", "Priya Shah",
			"Order 8832 — production update",
			"Hi Julian, good news — order 8832 (2,000 units M6 hex bolts) is confirmed and on schedule. Expected delivery is July 10, 2026. Best regards, Priya Shah, Sales Manager, Nordic Fasteners AB"},
		{convNordic8832, "cabrerajulian401@outlook.com", "Julian Cabrera",
			"Re: Order 8832 — production update",
			"Thanks Priya, we'll plan receiving for the 10th."},
		{convNordic8832, "priya.shah.nordic@gmail.com", "Priya Shah",
			"Re: Order 8832 — production update",
			"Julian, unfortunately the shipment hit a customs hold in Rotterdam. Revised delivery is July 22, 2026. Sincere apologies for the slip. Best regards, Priya Shah, Sales Manager, Nordic Fasteners AB"},
		{convDelmar, "marcus.lee.delmar@gmail.com", "Marcus Lee",
			"PO #4471 Confirmation — Delmar Tooling",
			"Hi Julian, confirming receipt of purchase order #4471 (40 units, carbide inserts). We expect to ship by July 15, 2026. Best, Marcus Lee — Sales, Delmar Tooling Co."},
		{convBrightline, "cabrerajulian401@outlook.com", "Julian Cabrera",
			"Quote request — 500 poly mailers",
			"Hi Tom, can you quote 500 poly mailers, 14.5x19, printed one color?"},
		{convBrightline, "tom.okafor.brightline@gmail.com", "Tom Okafor",
			"Re: Quote request — 500 poly mailers",
			"Sure — $0.42/unit, $210 total, 2-week lead time from order confirmation. Quote valid 30 days. Thanks, Tom Okafor, Account Manager, Brightline Packaging"},
	}
}

func TestFilterOrderMessagesLive(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("live LLM eval: set ANTHROPIC_API_KEY to run")
	}

	g := New("", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	msgs := fixtureMessages(t)

	wantKept := []string{convNordic8832, convDelmar, convBrightline}
	wantExcluded := []string{
		convMSApps1, convMSApps2, convMSApps3, convMSSignIn,
		convMaestro1, convMaestro2, convMaestro3, convMaestro4,
		convM365, convCongrats,
	}

	for trial := 1; trial <= filterTrials; trial++ {
		kept, err := g.FilterOrderMessages(context.Background(), msgs)
		if err != nil {
			t.Fatalf("trial %d: FilterOrderMessages: %v", trial, err)
		}
		got := make(map[string]bool, len(kept))
		for _, id := range kept {
			got[id] = true
		}
		for _, id := range wantKept {
			if !got[id] {
				t.Errorf("trial %d: dropped order conversation %s (%s)", trial, id[:12], subjectOf(id))
			}
		}
		for _, id := range wantExcluded {
			if got[id] {
				t.Errorf("trial %d: kept non-order conversation %s (%s)", trial, id[:12], subjectOf(id))
			}
		}
	}
}

func subjectOf(conv string) string {
	for _, f := range fixtureInbox() {
		if f.conv == conv {
			return f.subject
		}
	}
	return "?"
}

func fixtureMessages(t *testing.T) []entity.EmailMessage {
	t.Helper()
	base := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	inbox := fixtureInbox()
	msgs := make([]entity.EmailMessage, 0, len(inbox))
	for i, f := range inbox {
		msgs = append(msgs, entity.EmailMessage{
			ID:              f.conv[:16] + "-m" + string(rune('a'+i)),
			ConversationID:  f.conv,
			From:            f.from,
			FromName:        f.fromName,
			Subject:         f.subject,
			BodyPreview:     f.preview,
			CreatedDateTime: base.Add(time.Duration(i) * time.Hour),
		})
	}
	return msgs
}
