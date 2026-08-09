# Scout BE Dashboard Tech Spec

# **Problem statement**

# At ThirdEye we want to provide an ordered view dashboard.

# **Problem statement**

## **Stakeholders**

# *Julian*

## **Summary**

# This will serve as an extension to the current overseer backend that is optimized for a dashboard. There will be an addition to Auth and Accounts, business logicchanges within each of the 4 features, modifications to tables, and new tables.

First we wei

We will be inserting the frontend design into Claude and comparing it to current backend system to spot differences in entities/functionality to see what needs to be added. This will be for the 4 core features.

## **Goals**

- Create a Scalable Solution
- Figure out if a serverless or server solution is a better fit
- Mold this tech spec & its changes into the existing Overseer infrastructure
- Insert

## **Non-goals**

- Ignore out fundamentals of my system such as how we handle accounts/authentication or how coupled are features are within another
- Couple features that could change

# **Proposed Solution**

# Authentication & Accounts

Reqs:

- User signs on via Microsoft Outlook ID
- 1 user access
- Multi-tenant should have 100-1000 users at full scale
- Authorize permissions, such as Mail.read, Mail.send, Files.ReadWrite, offline_access, User.Read in drive & graph
- Should be able to handle msft personal accounts & msft business accounts via SSO on their own platforms.

### Framework Ideas:

Use MSAL Framework (Microsoft Authentication Library). Combination of OAuth (Authorization) + OpenID (Authentication).

MSAL allows:

- Sign in through microsoft
- Returns Access token, refresh token, ID, -> can insert into DB
- **Persisted graph refresh token through a background job**
    - Handles Graph permissions through offline_access to return a refresh token
- Use Go microsoft-authenticaion-library-for=go library

### MSAL steps

**Flow A - Login / consent**

1. Browser hits GET /auth/login. The **auth controller** asks the **auth usecase** for a Microsoft auth-code URL (scopes include offline_access), sets a state cookie for CSRF, and redirects the browser to Microsoft.
2. User signs in + consents on Microsoft's page (their own work tenant, or personal account). Microsoft redirects back to GET /auth/callback?code=...&state=....
3. The **callback controller** verifies state, then calls the auth usecase HandleCallback(code).
4. The usecase calls the **MSAL gateway** ExchangeCode(code). MSAL redeems the code → gets access + **refresh** + ID tokens → writes them into its in-memory cache. That write triggers the **cache accessor's Export**, which serializes the cache and calls the **token-cache repo** Save(homeAccountID, blob) → encrypted row in PSQL.
5. The usecase reads the ID-token claims (oid, tid, email, name) and **upserts the users row**, keyed on oid, recording the home_account_id.
6. The usecase issues a session; the controller sets the **httpOnly session cookie** and redirects to the dashboard.

**Flow B — Authenticated dashboard read (browser present)**

Session middleware validates the cookie → loads the user → the handler reads from the **orders repo (DB only)** and returns. No graph call, no token use—this is the fast read path we split out earlier.

**Flow C - Background worker**

**Notification arrives.** Because a subscription exists, Graph POSTs a change notification to /webhooks/graph. The controller: answers the validationToken handshake if present, verifies clientState, maps subscription_id → home_account_id, enqueues a job, and returns 200 fast.

**Worker gets a token.** It asks the token usecase: token := GetAccessToken(homeAccountID). *All the MSAL/refresh/cache detail is hidden inside that call.* If it can't produce one, flag the user for re-consent and stop.

**Worker does the work.** GET the message by id with the token → LLM delta-update → upsert orders → publish to WS

**Flow D - Acquiring a new message subscription**

- After user consents (end of login) call POST/subcriptions to make a graph subscription for that users mail box
- Store the subscription_id in a db with expiration timestamp

Notes:

A session cookie is a small piece of data your backend hands to the browser after you log in, which the browser then automatically sends back on every subsequent request. Cookies frequently store session ID’s.

**The Access Token**: This is your digital pass to view data. Because it is sent with every single API request, it is highly exposed. To protect you if it gets stolen, it is designed to expire very quickly—typically in **15 to 60 minutes**. [1, 2, 3, 4]

**The Refresh Token**: Instead of making you type your password every hour when that access token expires, the application silently sends your refresh token to the identity provider behind the scenes. **The provider verifies it and issues a brand-new access token.**

**A client secret is a confidential password an application uses to prove its identify to an authorization server.**

**clientState is a shared secret you invent and hand to Graph, which Graph hands you back.**

**Graph's clientState — your app (which, from Graph's perspective, is the *client*) stashes a value at subscription setup and Graph returns it on every notification, so your app can confirm continuity.**

Client state is simply the information a client (a browser, app, or program making requests) holds on its own side, as opposed to server state, which lives authoritatively in the backend database or session store. It exists because protocols like HTTP are stateless — the server doesn't inherently remember you between requests — so the "memory" of the current situation has to be parked somewhere: either the client carries it (form input, UI state like which tab is open, cached data, tokens, cookies, query parameters) or the server keeps it and the client just holds a lookup key. The state parameter in OAuth and Graph's clientState are narrow instances of this: a small piece of client-held information deliberately sent out and echoed back to re-establish context or verify continuity across a stateless round trip.

# Repo Layout (Rought Draft)

## **Scout — Auth & Endpoint Layout**

***Companion to Scout BE Tech Spec · MSAL / Graph auth flows A–D · Clean Architecture (feature-foldered usecases)***

---

### **Endpoint Map (summary)**

| **#** | **Endpoint** | **Direction** | **Auth** | **Flow** |
| --- | --- | --- | --- | --- |
| **1** | **GET controller/auth/Login** | **FE → BE** | **none (public)** | **A** |
| **2** | **GET controller/auth/Callback** | **MSFT → BE (redirect)** | **state cookie check** | **A** |
| **3** | **POST controller/auth/Logout** | **FE → BE** | **session cookie** | **A** |
| **4** | **GET controller/user/Me** | **FE → BE** | **session cookie** | **B** |
| **5** | **GET controller/user/Getorderboard** | **FE → BE** | **session cookie** | **B** |
| **6** | **WS controller/Websocket/UpdatedMessage** | **FE → BE** | **session at upgrade** | **B/C** |
| **7** | **POST controller/webhooks/graph** | **MSFT → BE** | **validationToken + clientState** | **C/D** |
| **8** | **POST controller/webhooks/graph/lifecycle** | **MSFT → BE** | **clientState** | **D** |
| **–** | **POST /subscriptions (Graph)** | **BE → MSFT** | **Graph access token** | **D** |
| **–** | **PATCH /subscriptions/{id} (Graph)** | **BE → MSFT** | **Graph access token** | **D** |
| **–** | **GET /users/{id}/messages/{msgId} (Graph)** | **BE → MSFT** | **Graph access token** | **C** |

***Dependency chain: A creates the token cache → D consumes tokens to keep subscriptions alive → C consumes both (subscription routes the notification, token cache authorizes the fetch) → B reads only what C wrote.***

---

### **Flow A — Login / Consent**

#### **`GET controller/auth/Login (FE→BE)`**

- **Generates random state, sets short-lived httpOnly oauth_state cookie (CSRF)**
- **Calls usecase/auth/LoginURL → MSAL builds authorize URL with scopes: Mail.Read, Mail.Send, Files.ReadWrite, User.Read, offline_access**
- **302 redirect to login.microsoftonline.com/common (multi-tenant + personal accounts)**

#### **`GET controller/auth/Callback (MSFT→BE)`**

- **Rejects if ?error= present (user declined consent)**
- **Verifies ?state= against cookie (constant-time compare), clears cookie — one-time use**
- **Calls usecase/auth/HandleCallback(code)**
- **On success: issues session, sets httpOnly session cookie, & continues to account creation - see below**

#### **`usecase/auth/HandleCallback`**

- **Calls MSAL gateway ExchangeCode(code) → access + refresh + ID tokens**
    - **Side effect: cache accessor Export → Save(home_account_id, encrypted_blob) → PSQL**
- **Upserts users row keyed on oid, records home_account_id**
- **Triggers Flow D: enqueue job/CreateMailSubscription(home_account_id)**
- **Returns user → controller issues session**

`GET controller/auth/Consent (FE→BE)` — **NEW USERS ONLY** *(onboarding Step 3, after profile is completed)*

- Second authorize redirect to Entra with full scopes: **Mail.Read, Mail.Send, Files.ReadWrite, offline_access** (this request is what yields the refresh token)
- Same callback handles the return (`state` carries `intent: consent`); MSAL exchange → Graph-scoped tokens saved to cache
- **Triggers Flow D here:** enqueue `job/CreateMailSubscription(home_account_id)` + initial sync job
- Records `consented_at` on user_logins → 302 /dashboard
- *Returning users never hit this — they consented at signup. (Exception: reused by the Reconnect flow when needs_reconsent = true.)*

#### **`POST controller/auth/Logout (FE→BE)`**

- **Deletes session row, clears cookie**
- **Offboarding variant: also delete subscription + token cache rows**

---

### **Flow B — Dashboard Reads (session only · DB only · no Graph)**

#### **`middleware/session`**

- **Validates httpOnly session cookie → loads user into request context → 401 if invalid**

#### **`GET controller/user/Me (FE→BE)`**

**{ UserID, Email, DisplayName, NeedsReconsent: bool }**

- **NeedsReconsent is how the FE learns a background refresh token died (Flow C flag surfaces here)**

#### **`GET controller/user/GetActiveOrderList (FE→BE)`**

- **Reads ONLY from Orders DB, filtered by session user. No LLM, no Graph — fast read path**
- **Payload: existing order JSON shape (see main Tech Spec)**

#### **`WS controller/Websocket/UpdatedMessage`**

- **Session validated at WS upgrade; client subscribed to its own user topic**
- **Receives order-update and discrepancy payloads pushed by workers**

---

### **Flow C — Change Notification → Worker**

#### **`POST controller/webhooks/graph (MSFT→BE)` — public URL**

**Guard sequence, in order:**

1. **?validationToken= present → echo back text/plain 200 within 10s (handshake)**
2. **Loop value[] — notifications arrive batched**
3. **Drop any entry where clientState != secret**
4. **Map subscription_id → home_account_id via subscriptions table; drop unknown IDs**
5. **Enqueue job/HandleChangeMessage{home_account_id, message_id} (unique per conversation)**
6. **Return 200 fast — no work inline**

***The notification is a poke, not a payload: it carries only an id + changeType. Real content is always re-fetched from Graph with our own token, so a forged notification cannot inject data.***

#### **`job/HandleChangeMessage`**

- **token := usecase/token/GetAccessToken(home_account_id) — auth black box (MSAL silent acquire, refresh, rotated cache re-saved)**
    - **On interaction-required error → set needs_reconsent = true on user, stop**
- **`GET /users/{id}/messages/{messageId} (BE→MSFT)` with token**
- **Load current order state by conversation_id → LLM delta-update (schema-validated)**
- **Upsert Orders — preserve OriginalEstDate, update CurrentEstDate**
- **After commit → publish to WS topic for that user**

---

## **Subscription Lifecycle**

#### **`job/CreateMailSubscription` (triggered end of login)**

- **token := GetAccessToken(home_account_id)**
- **POST https://graph.microsoft.com/v1.0/subscriptions (BE→MSFT):**

```jsx
{

changeType: "created",

notificationUrl:          "https://api.scout.app/webhooks/graph",

lifecycleNotificationUrl: "https://api.scout.app/webhooks/graph/lifecycle",**

resource: "/me/mailFolders('inbox')/messages",

expirationDateTime: now + ~4200 min,   // max < 3 days for messages**

clientState: SECRET

}
```

- **Graph immediately calls webhook with validationToken — guard step 1 must already be live**
- **Upsert into subscriptions table: (subscription_id, home_account_id, expires_at)**
- **Idempotency: one active subscription per user — check before creating**

#### **`job/RenewSubscriptions`(cron, every ~12h)**

- **Query subscriptions WHERE expires_at < now() + interval '24 hours'**
- **For each: GetAccessToken → PATCH /subscriptions/{id} with new expirationDateTime → update expires_at**
    - **PATCH extends the same subscription — same ID. Only on 404/failure: delete row, re-run CreateMailSubscription**
- **Token dead → flag needs_reconsent, skip**

***On Lambda/Step Functions: since Overseer already runs a worker + job queue for Flow C, an in-process cron is simpler than adding AWS infra (matches the 'no unneeded complexity' non-goal). Lambda only wins if the API server doesn't run 24/7.***

#### **`POST controller/webhooks/graph/lifecycle (MSFT→BE)`**

- **reauthorizationRequired → renew now**
- **subscriptionRemoved → recreate**
- **Missed notifications → trigger resync**

---

[](data:image/png;base64,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)

# Accounts & Account Creation

This will cover basic account operations as well as act as the datastore having several tables that will record consumer/account data.

Entity Questions:

- Account is the entity we want to model
- What fields or features do we want from a user that need to be recorded?
    - Access Tokens?
    - Login information?
    - Business data?
- What operations would we need?
    - Store new tenants and users on onboarding
    - Record user logins
    - Change account information
        - first_name
        - last_name
        - phone_number

### Tables:

```sql
CREATE TABLE tenants (
		id UUID PRIMARY KEY gen_random_uuid(),
		created_at TIMESTAMP NOT NULL, 
		updated_at TIMESTAMP NOT NULL,
		name text NOT NULL, 
		industry text,
)
```

```sql
CREATE TABLE users (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	created_at TIMESTAMP NOT NULL, 
	updated_at TIMESTAMP NOT NULL,
	diplay_name text,
	email text NOT NULL,
	tenant_id UUID REFERENCES tenants(id),
	first_name text,
	last_name text,
	phone_number text,
	is_active BOOLEAN,
)
```

```sql
CREATE TABLE user_logins (                   -- a WAY OF SIGNING IN (1 user : N logins)
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider           text NOT NULL,              -- 'entra' (later: 'external_id', 'google')
    provider_user_id   text NOT NULL,              -- oid for Entra; sub for others
    provider_tenant_id text,                       -- tid (Entra-specific, null elsewhere)
    home_account_id    text UNIQUE,                -- Entra-specific routing key
    needs_reconsent    BOOLEAN NOT NULL DEFAULT false,
    last_login_at      timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_user_id)            -- THE identity key
);
```

# Layout:

## Login & Account Flow

 Microsoft redirects back with a one-time `code`; the backend exchanges it for verified identity (`oid`, `tid`, `email`, `name`), then a single upsert decides new vs. returning.

**`GET /auth/callback`** (Microsoft → BE)

- Params: `?code=&state=` only — no identity fields; identity comes from the exchange, never the client
- Verify `state` cookie, reject `?error=` → `ExchangeCode(code)` → claims → `resolve_login.go` → issue session cookie
- Redirect:
    - **new / unfinished onboarding** → `/onboarding/profile`  `GET controller/auth/Consent (FE→BE)` → `job/create_subscription.go` → sync
    - **returning** → `/dashboard`

**`resolve_login.go`** (usecase — no route)

- One atomic upsert on `user_logins (provider, oid)`:
    - row exists → touch `last_login_at`, refresh email/name — *this is the login record*
    - no row → insert `users` + `user_logins`, one tx — *this is signup*
- Returns outcome: `NewUser` | `ResumeOnboarding` (`tenant_id` NULL) | `Returning`

**`POST /account/onboarding`** (FE, session)

- Step 2 form submit: create `tenants` + fill profile + set `tenant_id`, one tx → FE proceeds to consent/sync

`GET controller/auth/Consent (FE→BE)` — **NEW USERS ONLY** *(onboarding Step 3, after profile is completed*

**`GET /orderboard`** (FE, session)

- DB-only read; both branches land here

**Rules:** identity is born server-side · existence is checked on `user_logins`, not `users` · signup is just the first login's side effect.