# ThirdEye — Backend Tech Spec / PRD (Dashboard 2)

**Source of truth:** `ThirdEye Dashboard 2.html` (clickable prototype) + `ThirdEye Build Spec.html` (wireframe flows, Aug 2026).
**Purpose:** Complete inventory of core mechanics, entities/fields, and high-level functions in the new dashboard — for diffing against the existing backend to identify what to change or add. Field names below are taken **verbatim from the prototype's state model** so they can be grepped against the current schema.

---

## 1. Product Overview

ThirdEye reads a purchaser's vendor email (Microsoft Graph) and maintains a self-updating purchase-order board. Five modules sit on that ingestion pipeline:

| Module | One-liner |
|---|---|
| **Order Board** | Every open PO as a card: stage, ship date, value, issues. Fills itself from email. |
| **Needs Review** | Inbox emails that couldn't be matched to an order — assign / create order / dismiss. |
| **Otto** | Extraction review queue → writes approved rows to an Excel workbook on OneDrive. |
| **Checkups** | Scheduled vendor-chasing emails with triggers, send caps, and reply detection. |
| **Audit** | Discrepancy flags (price/qty/date/doc-mismatch) with an accept / dispute / dismiss resolution model. |

Supporting surfaces: **Contact List** (vendor identity), **Settings** (inbox + spreadsheet connections, column mapping), **Account** (profile, subscription, security).

**Scope constraints (from build spec):** single user per tenant; desktop-first (~1280px target); no permissions, no mobile. The accounts schema is multi-tenant-ready (`tenants` → `users`, 100–1000 users at full scale — see `In_Prog_Tech_Spec.md`), but the product UI ships single-user. All prototype figures are mock data.

---

## 2. Sign-in & Onboarding (Flow 1)

### Mechanics *(aligned to `In_Prog_Tech_Spec.md` Flows A–D — the source of truth for auth & accounts)*
1. **Get started** — "Continue with Outlook" → `GET /auth/login`: backend (MSAL confidential client) builds the Microsoft authorize URL, sets a short-lived httpOnly `oauth_state` cookie (CSRF), 302 to `login.microsoftonline.com/common` (work **and** personal Microsoft accounts). The business-email path is secondary prototype UI; the implemented flow is Microsoft sign-in.
2. **MS account chooser** — Microsoft's own UI (`KNOWN_ACCOUNTS` in the prototype is mock). The backend learns new-vs-returning at `GET /auth/callback`: verify `state` (constant-time, one-time), `ExchangeCode(code)` → ID-token claims (`oid`, `tid`, `email`, `name`) → `resolve_login`: **one atomic upsert on `user_logins (provider, provider_user_id)`** → outcome `NewUser | ResumeOnboarding | Returning`; httpOnly session cookie issued.
3. **Branch** — *Returning:* → `/dashboard` (profile prefilled, consent already on file, tutorial marked done). *New / unfinished:* → `/onboarding/profile` (profile → consent → sync → tutorial).
4. **Step 2 of 3 — About you** → `POST /account/onboarding`, one tx: create `tenants` row (`name` = business name, `industry`) + fill user profile (`first_name`, `last_name`, `phone_number`) + set `users.tenant_id`.
5. **Step 3 of 3 — Permissions** → `GET /auth/consent` (**new users only**, after profile): a second authorize redirect with the full scope set — `Mail.Read`, `Mail.Send`, `Files.ReadWrite`, `User.Read`, `offline_access` — this consent yields the refresh token (MSAL cache → encrypted row keyed `home_account_id`). On return: record `consented_at`, enqueue `job/CreateMailSubscription` + the initial sync job.
6. **Sync** — initial mailbox ingestion (~progress UI with 5 status lines + % counter). Nothing in the app shell renders until sync completes; board appears fully populated.
7. **Tutorial** — 8 steps over the live app; skippable; Account → "Replay tutorial" resets it.

### Backend implications
- **Identity is born server-side**: existence is checked on `user_logins (provider, provider_user_id = oid)` — never on `users.email`; `home_account_id` is the token-cache/webhook routing key; signup is just the first login's side effect (`resolve_login` upsert).
- Onboarding state derives from data, no status column: `users.tenant_id IS NULL` → resume at profile; `consented_at IS NULL` → resume at consent; else returning.
- Grant state = **one consent** (`consented_at`) + a `needs_reconsent` flag — set when silent token refresh dies (interaction-required), surfaced on `GET /user/me`, cleared by the Reconnect flow (reuses `/auth/consent`). Not per-scope booleans (`grantMail`/`grantFiles` in the prototype are mock).
- Session-cookie auth for every dashboard read (session middleware → DB-only fast path, no Graph); `/ws` validates the session at upgrade and scopes pushes to the user's own topic.
- Initial sync = one-time backfill job with progress reporting, **triggered by the consent callback** — not a separately gated action.
- `tutorialStep` / `tutorialDone` persisted per user (dashboard state; not covered by the auth spec).

---

## 3. Entities & Fields (verbatim from prototype state)

### 3.1 Order
```
order_number            string   'PO-2417' — display/business key
vendor_name             string   (FK → Vendor by name in prototype; use vendor_id in DB)
item_description        string   (denormalized copy of items[0] — see items)
quantity                int      (denormalized items[0])
unit_price              decimal  (denormalized items[0])
order_date              date
expected_ship_date      date|null   (derived: earliest items[].expected_ship_date)
order_stage             enum     Ordered | Confirmed | In Production | Shipped | Delivered
stage_source            enum     auto_inferred | manual_override
last_activity_date      date
last_activity_summary   string
source_email_link       url      (deep link to Outlook message)
stated_total            money|null  — total a vendor document claims; shown vs computed total
is_new                  bool     — arrived/changed since last viewed; cleared on open (per-user read state)
archived                bool     — Delivered orders auto-archive 30 days after delivery
delivered_date          date|null   (drives archive countdown)
contested               (derived from open Disputed flag → card shows CONTESTED)

items[]                 — NEW: multi-line orders (editable table in Order Detail)
  item_description      string
  quantity              int
  unit_price            decimal
  expected_ship_date    date|null
  (line_total derived = quantity × unit_price; order total = Σ line totals)

requested {}            — what we asked for:   quantity, unit_price, expected_ship_date
confirmed {}|null       — what vendor confirmed: quantity, unit_price, expected_ship_date
                          (drives "Requested vs. Confirmed" panel)

documents[]             — parsed attachments:
  doc_type              enum   Invoice | Packing List | BOL
  filename              string
  received              date
  reconciliation_status enum   Pending | Mismatch | Reconciled
  parsed {}             quantity, unit_price, total, expected_ship_date
                        (drives per-field "In document / On order" compare view)

timeline[]              — activity log (append-only):
  date                  date
  kind                  enum   email | stage | flag | checkup | edit
  text                  string
  link                  url|null
```

### 3.2 Extraction (Otto queue item)
```
id, source_email_sender, source_email_subject, received_date
source_type             enum   'Email Text' | 'Document (OCR)'
item_description, quantity|null, unit_price|null, expected_ship_date|null
confidence_level        enum   High | Medium | Low
extraction_status       enum   Pending Review | Accepted | Corrected | Rejected
erp_sync_status         enum   Not synced | Retrying | Synced | Sync Failed
sync_error              string|null   (e.g. 'Excel Online returned 423 Locked …')
synced_at               date|null
sheet_row               int|null      (row actually written)
retries                 int
target_order            string|null   (PO the extraction applies to; null → will create new order)
evidence_snippet        string        (quoted source text)
evidence_where          string        ('Email body, paragraph 2' / 'file.pdf · page 1, line 14')
currency                string|null   (non-USD triggers convert warning)
source_email_link       url
```

### 3.3 Checkup
```
id, vendor_name (→ vendor_id)
message_template        string
attachment              filename|null
trigger_type            enum   cadence | silent | preship        ← NEW (3 trigger types)
trigger_value           int    (N for the trigger)
send_frequency, freq_unit ('days'|'weeks')   (legacy cadence fields; freqDays() = freq*7 if weeks)
send_window             enum   'Weekdays only' | 'Any day'
order_numbers[]         POs covered (multi-PO checkups supported; one message or tailored per-PO)
next_send_date          date|null
last_sent_date          date|null
last_reply_date         date|null
last_reply_text         string      (quoted vendor reply shown in UI, with thread link)
checkup_status          enum   Active | Reply Received | Paused | Awaiting Manual Check
sends_since_reply       int         (send cap = 2; bar shows 3 segments per vendor)
paused_by_dispute       bool        ← NEW (dispute pauses vendor's checkups; close resumes)
history[]               { date, dir: 'Sent'|'Reply', text, body? }
```

### 3.4 Audit Flag (Discrepancy)
```
id, vendor_name, order_id (PO)
discrepancy_type        enum   Price | Quantity | Date | Document Mismatch | Other
original_value, new_value   (display strings: '$2.40' → '$2.65', 'Aug 12' → 'Aug 20')
evidence_source         enum-ish   'Email Text' | 'Invoice' | 'Packing List' | 'BOL'
evidence_snippet        string     (verbatim quote)
evidence_link           url
flagged_date            date
flag_status             enum   New | Disputed | Accepted | Resolved | Dismissed | Reviewed
disputed_since          date|null
confidence_level        enum   High | Medium | Low
note                    string     (user note on the flag)
```

### 3.5 Needs Review (unmatched email)
```
id, source_email_sender, source_email_subject, received_date, source_email_link
parsed {}               best-effort pre-parse: vendor_name, item_description, quantity, unit_price
dismissed               (moves to recoverable dismissed list — nothing destroyed)
```

### 3.6 Vendor (Contact List)
```
id, vendor_name
emails[]                multiple addresses per vendor
contact_name            string
last_activity_date      date
(derived per vendor: activeOrders count, flagCount, checkupStatus)
```

### 3.7 Unknown Sender
```
email, count            (emails received from address not attached to any vendor)
— must be added as vendor (or ignored) before Otto writes / Checkups count replies
```

### 3.8 Connections & Mapping (Settings)
```
inbox connection        status (connected/disconnected), account email, queuedEmails count while down
sheet connection        status, sheetFile ('Order Tracker 2026.xlsx'), sheetTab ('Sheet1')
mapping {}              per-workbook column mapping, ThirdEye field → sheet header:
                          order_number, vendor_name, item_description, quantity, unit_price,
                          total_value, order_date, expected_ship_date, order_stage
                        ('don't sync this field' allowed per field)
destOverride            per-extraction destination override { file, tab }  ← NEW
WORKBOOKS               known workbooks w/ tabs[] + mapped flag (unmapped → must map before first write)
```

### 3.9 User / Account (per `In_Prog_Tech_Spec.md` — source of truth)
```
tenants        id, name (business name), industry
users          id, tenant_id (NULL until onboarding Step 2), display_name, email,
               first_name, last_name, phone_number, is_active
user_logins    provider ('entra'), provider_user_id (oid) — THE identity key (UNIQUE pair),
               provider_tenant_id (tid), home_account_id (UNIQUE — token-cache/webhook routing),
               needs_reconsent, consented_at, last_login_at        (1 user : N logins)
sessions       httpOnly cookie ↔ server-side session row; logout deletes the row
token cache    encrypted MSAL cache blob keyed home_account_id (refresh tokens at rest)
tutorialStep, tutorialDone   (per user — dashboard state, kept from prototype)
subscription: billing (Free Beta / Pro, Stripe) — deferred; prototype markup only, not in the auth spec
security: password/2FA — Microsoft's domain (display only)
```

---

## 4. Status Models & Enums (consolidated)

| Domain | Values | Transitions / notes |
|---|---|---|
| Order stage | Ordered → Confirmed → In Production → Shipped → Delivered | Inferred from email unless manual override; manual change stamps `stage_source: manual_override` + timeline entry. Delivered auto-archives after 30 days. |
| Extraction status | Pending Review → Accepted / Corrected / Rejected | Reject undoable 10s; rejecting a **Synced** row asks whether to remove the row from Excel. |
| Sync status | Not synced → Retrying → Synced / Sync Failed | `sync_error` holds provider error; retry increments `retries`. |
| Checkup status | Active ↔ Reply Received ↔ Paused; → Awaiting Manual Check | Reply cancels pending send + resets counter. 2 sends w/o reply → Awaiting Manual Check (halt, pin to top, "Send one more" overrides cap). Dispute → Paused (`paused_by_dispute`); close resumes. |
| Flag status | New → Accepted / Disputed / Dismissed; Disputed → (backed down → Resolved) / (held firm → runs Accept) / (withdraw → New) | Accepted/Resolved/Dismissed terminal but reopenable. Audit tabs: All · New · Disputed · Accepted · Closed (Resolved + Dismissed). |
| Doc reconciliation | Pending / Mismatch / Reconciled | Accepting a flag marks the evidence document Reconciled. |
| Confidence | High / Medium / Low | On extractions and flags. |

---

## 5. Business Rules & Derived Values (backend must compute)

### Board metrics (stat tiles — the bordered ones are click-to-filter)
- `active_order_count` (IN FLIGHT): non-archived, stage ≠ Delivered
- `total_open_value` (ON ORDER): Σ order totals of active orders
- `stalled_order_count` (STALLED, filter): **stalled = no activity for ≥ 21 days** (`STALL_DAYS = 21`), stage ≠ Delivered, not archived
- `overdue_order_count` (PAST SHIP, filter): `expected_ship_date < today` and stage not Shipped/Delivered
- `open_flag_count_total` (OPEN ISSUES, filter): flags with status New across active orders

### Card display rules
- Ship figure priority: **N days late** (overdue) → **Nd silent** (stalled) → **In transit** (shipped) → **N days** → **TBD**. Bold only late/silent.
- **AT STAKE** shown only when the order has open discrepancies (Σ flagImpact of its New flags).
- Default sort `last_activity_date`; **new arrivals (`is_new`) pin above sort until opened**; opening clears `is_new`.
- Sorts: Last Update / Expected Ship / Order Value / Vendor / Status. Filters: stage, vendor, stat tiles, free-text query (order_number + vendor + item).

### Flag impact ("at stake" math — `flagImpact`)
- **Price:** |new − old| × order quantity
- **Quantity:** |new − old| × (order total ÷ order qty)
- **Document Mismatch / Other:** |new − old| (dollar values)
- **Date:** day delta — **no dollar value; sorts last** in exposure lists

### Audit exposure
- `Total Exposure` = Σ impact over **New flags only**; grouped (per-order) totals must reconcile with the header figure. Feed sorts newest first; resolved flags fade, lose quote + actions.
- `vendorPattern`: ≥2 same-type flags for a vendor → "X has raised prices on N orders this quarter."

### Checkups
- Triggers: **Every N days** (cadence) · **When silent for N days** (silent) · **N days before Expected Ship** (preship).
- Send cap: 2 sends since last reply → halt (`Awaiting Manual Check`). Reply cancels pending send and resets `sends_since_reply`.
- Schedule view ordered by `next_send_date`; paused sort last.
- **Coverage gaps**: open orders not covered by any checkup's `order_numbers`, grouped by vendor → "Not covered" prompts.
- Send window respected (weekdays vs any day).

### Vendor identity rule
A sender address must belong to a vendor **before** Otto extracts from it or Checkups count its replies. Unknown senders surface in Contact List (and as a blocking Otto issue) until added or ignored.

### Otto write pipeline (per extraction)
1. **Issues gate** (`queueIssues`) — computed per extraction:
   - **Blocking:** spreadsheet disconnected · sender not a known vendor · missing field (qty/price null)
   - **Warning:** foreign `currency` (convert to USD at document rate) · two pending extractions targeting the same order (compare) · likely duplicate of an already-written row (discard)
2. **Destination** is per-extraction (`destOverride`): any workbook, any tab. An unmapped workbook must collect a column mapping before its first write.
3. **Confirmation** must name insert-vs-update and the row number. Row numbers advisory — sheet re-checked at write time (`existingSheetRow` for the PO, else `nextSheetRow`).
4. **On accept:** row written → **order created or updated on the board** (`applyExtractionToBoard`: updates items[0] + confirmed{}; or creates a new order at stage Ordered) → timeline entry → toast with "Open order".
5. Multi-item orders: offer one row per item or one row for the whole order.
6. New order from extraction with no vendor match: vendor inferred from sender domain.

### One order, one truth (cross-cutting)
An accepted change lands in **all three places in one transaction**: the order record, the board card, and the queued spreadsheet row. No screen may show a stale figure another screen has updated. (Accept flow: order updated + document marked Reconciled + timeline entry + Otto row queued — Excel still requires its own confirmation in Otto.)

### Confirmations required before
Writing to the spreadsheet · rejecting a synced row · sending a dispute · archiving · deleting a checkup · overriding the send cap · disconnecting an integration · dismissing an unmatched email.

### Undo & recoverability
- 10-second undo: rejecting an extraction, dismissing a flag; skip-a-send also undoable.
- Dismissed needs-review emails and dismissed flags move to **recoverable lists** (Reopen/Restore) — nothing destroyed.

### Disconnected states
- **Inbox down:** dark banner + count of queued unprocessed emails; on reconnect, backfill processes the queue.
- **Sheet down:** amber banner; Otto **holds extractions and blocks writing** (blocking issue) until reconnected.

---

## 6. High-Level Functions (operation inventory)

Grouped by module; names from the prototype in parentheses. This is the API/usecase surface to diff against the current backend.

### Auth & Onboarding (per `In_Prog_Tech_Spec.md` Flows A–D)
- `GET /auth/login` (state cookie, authorize redirect) · `GET /auth/callback` (state check → `ExchangeCode` → `resolve_login` upsert → `NewUser | ResumeOnboarding | Returning` → session) · `POST /auth/logout` (delete session row; offboarding variant also removes subscription + token cache)
- `POST /account/onboarding` (Step 2: tenant + profile, one tx) · `GET /auth/consent` (Step 3, new users only: full-scope authorize → refresh token → `consented_at`; triggers `job/CreateMailSubscription` + initial sync; reused by Reconnect when `needs_reconsent`)
- `GET /user/me` → `{ userId, email, displayName, needsReconsent }` · tutorial state
- Background: `GetAccessToken(home_account_id)` (MSAL silent/refresh, rotated cache re-saved; interaction-required → `needs_reconsent = true`, stop) · `job/RenewSubscriptions` (~12h cron, renews subs expiring < 24h) · `POST /webhooks/graph/lifecycle` (reauthorizationRequired → renew · subscriptionRemoved → recreate · missed → resync)

### Order Board / Order Detail
- List orders w/ filters, sorts, query, stat-tile filters (`boardVals`)
- Get order detail: summary, items, requested vs confirmed, documents, discrepancies, timeline (`detailVals`)
- Edit order fields incl. targeted requested/confirmed side (`editOrderField`); item add/remove; draft-until-Save; Save writes all changes + **one** timeline entry
- Manual stage override (stamps `stage_source`, timeline entry)
- Clear `is_new` on open · archive countdown (`archiveDaysLeft`) · auto-archive job (Delivered + 30d)
- Document compare view (parsed vs order, per field)

### Needs Review
- List unmatched emails + dismissed list
- Assign to order (`assignEmail`): timeline entry on target + push Pending Review extraction into Otto queue
- Create order from email (`createOrderFromDraft`): prefilled modal; Vendor/Item/Qty required; starts Ordered + manual_override; email = first timeline entry
- Dismiss (confirmation, recoverable) / Reopen

### Otto
- List queue by tab (All/Pending/etc.), stats line, badge = pending count
- Compute issues per extraction (`queueIssues`) + resolve actions (`resolveIssue`): reconnect, add vendor, fill field, convert currency (rate from document), compare twin, discard dupe
- Destination picker (`openWriteFlow`, `destOverride`, WORKBOOKS/tabs, unmapped → mapping wizard)
- Accept & write (`writeToSheet`): insert-vs-update confirm → sheet write → board apply (`applyExtractionToBoard`) → timeline → toast
- Correct (edit extracted fields, then write as Corrected) · Reject (`rejectRow`, undo 10s, synced-row removal prompt) · Retry sync (`retrySync`, error surfaced from provider)

### Checkups
- CRUD checkup (`saveCheckupForm`): vendor, message, attachment, trigger (3 types), send window, covered POs (one or several)
- Schedule computation: next_send ordering, paused last, today expanded
- Send now · Skip (undo) · Pause/Resume · Delete (confirm) · History
- Send-cap enforcement + "Send one more" override (confirm)
- Reply detection: cancels pending send, resets counter, records `last_reply_*`, reschedules
- Dispute pause/resume hooks (`paused_by_dispute`)
- Coverage gaps (`coverageGaps`) → "Set up checkup" prompts

### Audit
- Feed w/ tabs, group-by-order toggle, exposure totals (`auditVals`)
- Accept change (`acceptFlag`): order items updated by type, `confirmed{}` updated, doc → Reconciled, `stated_total` cleared, timeline, Otto row queued, `is_new` set
- Dispute (`openDispute`/`sendDispute`): 3-step — compose (drafted from flag: your figure, theirs, difference, quote), confirm & send (order contested, original value kept, nothing written to sheet, vendor checkups paused), close (`closeDispute`: backed_down → Resolved · held_firm → runs Accept · withdraw → New; all resume checkups)
- Dismiss (undo 10s, recoverable) / Restore / Reopen · note save · draft-email-to-vendor deep link (`draftLinkFor`)

### Contact List
- List vendors w/ derived counts (orders, issues, last update, checkup status)
- Add vendor · add email to vendor · unknown senders: add-as-vendor / ignore
- View orders (filter board by vendor)

### Connections / Settings
- Inbox: status, reconnect (processes queued emails), disconnect (confirm)
- Spreadsheet: pick file (recent OneDrive list or **create tracking sheet** — skips mapping), map columns (guess prefill + 3-row live preview + don't-sync-per-field), confirm (states exactly which columns are written; never touches other columns/formatting/rows), change mapping/file, disconnect (confirm)

### Account
- Profile display/edit · replay tutorial · sign out
- Subscription: manage billing / view invoices (delegated to billing provider)

---

## 7. Delta Checklist — new dashboard vs. the existing (Scout-era) backend

Features in Dashboard 2 that the old backend spec (single-item orders, basic queue/flags/checkups) likely does **not** cover — the diff list to walk your repo with:

**Auth & Accounts** (source of truth: `In_Prog_Tech_Spec.md`)
- [ ] Session-cookie auth: `sessions` table + session middleware; `/ws` session check at upgrade + per-user topics
- [ ] MSAL confidential client (auth-code flow) + persisted **encrypted token cache** keyed `home_account_id`
- [ ] `tenants` / `users` / `user_logins` schema — identity on `(provider, oid)`, existence checked on `user_logins`
- [ ] Two-step consent onboarding: sign-in → profile (`POST /account/onboarding`) → full-scope consent (`GET /auth/consent`) → subscription + sync
- [ ] `needs_reconsent` flag + Reconnect flow; surfaced on `GET /user/me`
- [ ] Graph **lifecycle notifications** endpoint (`/webhooks/graph/lifecycle`) + ~12h renewal cron

**Orders**
- [ ] Multi-line `items[]` per order (editable, add/remove, line totals; order totals derived)
- [ ] `requested{}` vs `confirmed{}` snapshots per order
- [ ] `documents[]` with `doc_type`, `reconciliation_status`, per-field `parsed{}` compare
- [ ] `stated_total` (vendor-claimed total vs computed) & mismatch banner
- [ ] `stage_source` (auto_inferred vs manual_override) + stage inference pipeline
- [ ] `is_new` per-user read state; new-arrival pinning
- [ ] Stalled tracking (21-day silence) + STALLED stat tile
- [ ] Delivered auto-archive after 30 days (`delivered_date`, archive job, countdown)
- [ ] Contested state driven by Disputed flags

**Needs Review**
- [ ] Unmatched-email entity + pre-parse; assign→queue chaining; recoverable dismissed list

**Otto**
- [ ] Per-extraction destination override (multi-workbook, multi-tab)
- [ ] Per-workbook column mappings; unmapped-workbook gate before first write
- [ ] Issues engine: 3 blocking + 3 warning conditions incl. currency conversion, twin-extraction conflict, duplicate detection
- [ ] Insert-vs-update row logic; row re-check at write time; sheet_row tracking
- [ ] `sync_error` surfacing (e.g. Excel 423 Locked), retries, Retrying state
- [ ] Reject-synced-row → remove row from workbook option; 10s undo
- [ ] evidence_snippet / evidence_where provenance on extractions

**Checkups**
- [ ] Three trigger types (cadence / silent / preship) — old model was cadence-only
- [ ] Send cap (2 since reply) + Awaiting Manual Check + override
- [ ] Reply detection cancels send + resets counter
- [ ] Multi-PO coverage (`order_numbers[]`) + coverage-gap computation
- [ ] `paused_by_dispute` linkage to Audit
- [ ] Send window, attachments, per-checkup history log

**Audit**
- [ ] Full status machine incl. Disputed lifecycle (3-step dispute, outcomes)
- [ ] Flag impact math per type; Total Exposure (New only); date flags sort last
- [ ] Accept → transactional fan-out (order + doc + timeline + Otto row)
- [ ] Vendor pattern detection (≥2 same-type flags)
- [ ] Notes on flags; recoverable dismissed list

**Vendors**
- [ ] Vendor entity with multiple emails; unknown-sender workflow; identity gate for Otto/Checkups

**Cross-cutting**
- [ ] "One order, one truth" transactional consistency requirement
- [ ] Confirmation + undo inventory (Section 5)
- [ ] Inbox-down email queue + reconnect backfill; sheet-down write blocking
- [ ] Timeline as append-only activity log with `kind` enum across all mutations

---

*Companion doc in repo: `docs/In_Prog_Tech_Spec.md` — the **source of truth for auth & accounts** (MSAL/Graph flows A–D, subscription lifecycle, `tenants` / `users` / `user_logins` schema). This spec covers the product domain on top of that foundation; wherever the two disagree on auth or accounts, `In_Prog_Tech_Spec.md` wins.*