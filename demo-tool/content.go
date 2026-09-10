package main

// Demonstration content for the Keryx demo site, based on real public
// Trezor blog posts (https://trezor.io/blog, https://trezor.io/cs/blog).
// Titles and publication dates are real; the excerpts are abridged and, for
// the Czech items, translated by us for the demo. Every URL in the artifact
// points at the demo server (http://10.110.147.178:8000 — the host's
// tethering interface, a local-dev exception: HTTP is allowed only on
// loopback/RFC 1918 and Android permits cleartext for this demo host only);
// the original post URLs are kept in `source` for attribution (shown as
// plain text on the local permalink pages, never as links inside the signed
// content).
// This is demonstration data only — not published by Trezor.

const (
	companyName    = "Trezor Company s.r.o."
	companyHome    = "http://10.110.147.178:8000/"
	logoURL        = "http://10.110.147.178:8000/media/logo.png"
	metadataOrigin = "http://10.110.147.178:8000"
	// repoBase is the master-signed root.json custom.repo_base (PROTOCOL §3):
	// the TUF repo location, discovered from the well-known root anchor on
	// the join origin (/.well-known/keryx/root.json).
	repoBase       = metadataOrigin + "/keryx/"
	trackingPattern = metadataOrigin + "/channels/tracking/*/feed.json"
)

// channel describes one Keryx channel = a delegated TUF role (PROTOCOL §4–§6).
type channel struct {
	Name           string
	DisplayName    string
	Description    string
	DefaultConsent bool
}

var channels = []channel{
	{
		Name: "security", DisplayName: "Security alerts",
		Description:    "Phishing warnings, security advisories and key announcements",
		DefaultConsent: true,
	},
	{
		Name: "news", DisplayName: "Product news & updates",
		Description:    "New releases, features and company announcements",
		DefaultConsent: false,
	},
	{
		Name: "insights", DisplayName: "Insights & guides",
		Description:    "Self-custody guides, explainers and opinion",
		DefaultConsent: false,
	},
}

// editorConfig describes the editor-mode entry for one channel (PROTOCOL §9):
// which editor keys may author items and how many valid signatures are
// required (n-of-m threshold).
type editorConfig struct {
	KeyNames  []string // key names in the demo keys map
	Threshold int
}

// editorChannels: channels running editor mode. Master-signed via
// targets.json custom.editor_mode; the channel key only publishes.
var editorChannels = map[string]editorConfig{
	"security": {KeyNames: []string{"editor-security-a", "editor-security-b"}, Threshold: 2},
}

// trackingPatternEntry describes the private (per-order) capability feed
// authorization (PROTOCOL §10): a master-signed pattern entry.
var trackingPatternEntry = struct {
	Channel     string
	DisplayName string
	Purpose     string
}{
	Channel:     "tracking",
	DisplayName: "Package tracking",
	Purpose:     "per-order delivery notifications (capability feed)",
}

type demoItem struct {
	ID          string
	Channel     string
	Language    string
	Title       string
	Slug        string // local permalink page name: /blog/<slug>.html
	Source      string // original public post URL (attribution only)
	Author      string
	Summary     string
	ContentText string
	ContentHTML string
	Image       string // file name under /media/img/
	Published   string // RFC 3339
	Tags        []string
}

// publicItems are the demo items for the public channels (security/news/
// insights), newest last in this list.
var publicItems = []demoItem{
	{
		ID: "msg-2026-03-17-own-your-coins", Channel: "insights", Language: "en",
		Title:  "Own your coins 100%: A quick guide to self-custody",
		Slug:   "own-your-coins-100-a-quick-guide-to-self-custody",
		Source: "https://trezor.io/blog/insights/own-your-coins-100-a-quick-guide-to-self-custody",
		Author: "Trezor Team", Published: "2026-03-17T09:00:00Z",
		Summary:     "Self-custody means your keys, your coins. A practical guide to moving off exchanges onto a hardware wallet, setting up a backup, and why \"not your keys, not your coins\" is more than a slogan.",
		ContentText: "Self-custody means your keys, your coins. A practical guide to moving off exchanges onto a hardware wallet, setting up a backup, and why \"not your keys, not your coins\" is more than a slogan.",
		ContentHTML: "<p>Self-custody means your keys, your coins. A practical guide to moving off exchanges onto a hardware wallet, setting up a backup, and why \u201cnot your keys, not your coins\u201d is more than a slogan.</p>",
		Image:       "selfcustody.jpg",
		Tags:        []string{"self-custody", "security", "en"},
	},
	{
		ID: "msg-2026-03-18-phishing-attacks", Channel: "security", Language: "en",
		Title:  "Phishing attacks and how to keep your crypto safe",
		Slug:   "phishing-attacks-and-how-to-keep-your-crypto-safe",
		Source: "https://trezor.io/blog/security/phishing-attacks-and-how-to-keep-your-crypto-safe",
		Author: "Trezor Team", Published: "2026-03-18T10:30:00Z",
		Summary:     "Phishing is still the most common way crypto is stolen. Learn to spot fake emails, fake websites and fake support — and the simple habits that keep your funds safe.",
		ContentText: "Phishing is still the most common way crypto is stolen. Learn to spot fake emails, fake websites and fake support — and the simple habits that keep your funds safe. Never enter your wallet backup anywhere except your genuine Trezor device, and never sign a transaction that a link asks for.",
		ContentHTML: "<p>Phishing is still the most common way crypto is stolen. Learn to spot fake emails, fake websites and fake support — and the simple habits that keep your funds safe.</p>",
		Image:       "phishing.webp",
		Tags:        []string{"phishing", "security", "en"},
	},
	{
		ID: "msg-2026-03-25-how-crypto-is-stolen-en", Channel: "security", Language: "en",
		Title:  "This is the way most crypto is stolen",
		Slug:   "this-is-the-way-most-crypto-is-stolen",
		Source: "https://trezor.io/blog/security/this-is-the-way-most-crypto-is-stolen",
		Author: "Trezor Team", Published: "2026-03-25T14:00:00Z",
		Summary:     "Scammers are impersonating Trezor in a massive blast to users. The trick is manufactured urgency: an official-looking email that insists you act right now, betting you'll panic and hand over control of your wallet. The fix is one rule — never share your wallet backup with anyone.",
		ContentText: "While we were in the middle of filming our latest video, we got word that scammers were at it again — impersonating us in a massive blast to Trezor users. The trick here is all about \"manufactured urgency.\" You get an email that looks official and insists you act right now. The scammer is betting that the fear of losing your money will make you panic and follow their instructions without stopping to think, eventually handing over total control of your wallet.\n\nThe solution is actually pretty simple: never, under any circumstances, share your wallet backup with anyone. If you can stick to that one rule, you'll probably never lose your funds to a scam. Red flags to watch for: fake update links, fake devices on random sites, fake websites and search results, targeted phishing and spoofing, and threats of \"blocking\" or \"deactivating\" — a hardware wallet cannot be blocked or turned off remotely.",
		ContentHTML: "<p>Scammers are impersonating Trezor in a massive blast to users. The trick here is all about \u201cmanufactured urgency\u201d: an official-looking email that insists you act right now, betting you'll panic and hand over total control of your wallet.</p><p>The solution is simple: <strong>never, under any circumstances, share your wallet backup with anyone.</strong> Red flags: fake update links, fake devices, fake websites and search results, targeted phishing, and threats of \u201cblocking\u201d — a hardware wallet cannot be blocked remotely.</p>",
		Image:       "howstolen.jpg",
		Tags:        []string{"phishing", "self-custody", "en"},
	},
	{
		ID: "msg-2026-03-25-how-crypto-is-stolen-cs", Channel: "security", Language: "cs",
		Title:  "Takto se krade nejvíc kryptoměn",
		Slug:   "takto-se-krade-nejvic-kryptomen",
		Source: "https://trezor.io/cs/blog/security/this-is-the-way-most-crypto-is-stolen",
		Author: "Trezor Team", Published: "2026-03-25T14:00:00Z",
		Summary:     "Podvodníci se opět vydávají za Trezor a masově útočí na uživatele. Trik je \"vyrobená naléhavost\": e-mail, který vypadá oficiálně a nutí vás jednat okamžitě. Řešení je jediné pravidlo — zálohu své peněženky nikdy nikomu nesdělujte.",
		ContentText: "Podvodníci se opět vydávají za Trezor a masově útočí na uživatele e-maily s falešnou naléhavostí. Vsázejí na to, že strach ze ztráty peněz vás donutí jednat bez přemýšlení a předat jim kontrolu nad peněženkou.\n\nŘešení je jednoduché: nikdy, za žádných okolností, nikomu nesdělujte zálohu své peněženky. Červené vlajky: falešné odkazy na aktualizace, falešná zařízení, falešné weby a výsledky vyhledávání, cílený phishing a výhrůžky \"zablokováním\" — hardwarovou peněženku nelze na dálku zablokovat.",
		ContentHTML: "<p>Podvodníci se opět vydávají za Trezor a masově útočí na uživatele. Trik je \u201cvyrobená naléhavost\u201d: e-mail, který vypadá oficiálně a nutí vás jednat okamžitě.</p><p>Řešení je jednoduché: <strong>zálohu své peněženky nikdy nikomu nesdělujte.</strong></p>",
		Image:       "howstolen.jpg",
		Tags:        []string{"phishing", "self-custody", "cs"},
	},
	{
		ID: "msg-2026-05-11-quantum-deadline", Channel: "news", Language: "en",
		Title:  "Google just changed the quantum deadline. Here's what it means for your bitcoin…",
		Slug:   "google-just-changed-the-quantum-deadline",
		Source: "https://trezor.io/blog/news/Google-just-changed-the-quantum-deadline-Heres-what-it-means-for-your-bitcoin",
		Author: "Henry Windle", Published: "2026-05-11T08:00:00Z",
		Summary:     "Google's updated quantum-computing timeline has the crypto world talking. Here's what it actually means for your bitcoin today — and why the answer is preparation, not panic.",
		ContentText: "Google's updated quantum-computing timeline has the crypto world talking. Here's what it actually means for your bitcoin today — and why the answer is preparation, not panic.",
		ContentHTML: "<p>Google's updated quantum-computing timeline has the crypto world talking. Here's what it actually means for your bitcoin today — and why the answer is preparation, not panic.</p>",
		Image:       "quantum.jpg",
		Tags:        []string{"bitcoin", "security", "en"},
	},
	{
		ID: "msg-2026-05-12-wallet-screen", Channel: "insights", Language: "en",
		Title:  "Why your hardware wallet needs a screen",
		Slug:   "why-your-hardware-wallet-needs-a-screen",
		Source: "https://trezor.io/blog/insights/Why-your-hardware-wallet-needs-a-screen",
		Author: "Henry Windle", Published: "2026-05-12T09:00:00Z",
		Summary:     "A hardware wallet is only as trustworthy as what you can verify on it. The screen is the part you trust: review every transaction on the device before signing. Anything less is blind trust.",
		ContentText: "A hardware wallet is only as trustworthy as what you can verify on it. The screen is the part you trust: review every transaction on the device before signing. Anything less is blind trust.",
		ContentHTML: "<p>A hardware wallet is only as trustworthy as what you can verify on it. The screen is the part you trust: review every transaction on the device before signing. Anything less is blind trust.</p>",
		Image:       "screen.jpg",
		Tags:        []string{"self-custody", "security", "en"},
	},
	{
		ID: "msg-2026-05-28-stablecoin-yield", Channel: "news", Language: "en",
		Title:  "A safer way to earn yield with stablecoins in Trezor Suite",
		Slug:   "a-safer-way-to-earn-yield-with-stablecoins-in-trezor-suite",
		Source: "https://trezor.io/blog/news/a-safer-way-to-earn-yield-with-stablecoins-in-trezor-suite",
		Author: "Trezor Team", Published: "2026-05-28T10:00:00Z",
		Summary:     "Earn yield on stablecoins directly in Trezor Suite — with the same self-custody guarantees. Your keys stay in your Trezor; only transactions you approve on-device ever move funds.",
		ContentText: "Earn yield on stablecoins directly in Trezor Suite — with the same self-custody guarantees. Your keys stay in your Trezor; only transactions you approve on-device ever move funds.",
		ContentHTML: "<p>Earn yield on stablecoins directly in Trezor Suite — with the same self-custody guarantees. Your keys stay in your Trezor; only transactions you approve on-device ever move funds.</p>",
		Image:       "stablecoin.jpg",
		Tags:        []string{"trezor-suite", "product", "en"},
	},
	{
		ID: "msg-2026-06-30-suite-sync", Channel: "news", Language: "en",
		Title:  "Permissionless storage with Suite Sync",
		Slug:   "permissionless-storage-with-suite-sync",
		Source: "https://trezor.io/blog/news/permissionless-storage-with-suite-sync",
		Author: "Henry Windle", Published: "2026-06-30T12:00:00Z",
		Summary:     "Suite Sync is now live in Trezor Suite on desktop and mobile. Wallet labels are encrypted on your device with keys derived from your wallet backup, synced across every device, and stored on Trezor's own open-source server — not Google's or Dropbox's. Migration is a single action; users who want no dependency on Trezor can self-host.",
		ContentText: "Suite Sync is now live in Trezor Suite on desktop and mobile. Your wallet labels are encrypted on your device using keys derived from your wallet backup, synced across every device you use, and stored on Trezor's own server, not Google's or Dropbox's. If you're already using legacy labeling, migration is a single action. Users who want to remove any dependency on Trezor can run their own server.",
		ContentHTML: "<p>Suite Sync is now live in Trezor Suite on desktop and mobile. Your wallet labels are encrypted on your device using keys derived from your wallet backup, synced across every device you use, and stored on Trezor's own open-source server — not Google's or Dropbox's.</p><p>Migration from legacy labeling is a single action, and advanced users can self-host to remove any dependency on Trezor.</p>",
		Image:       "suitesync.jpg",
		Tags:        []string{"trezor-suite", "privacy", "product", "en"},
	},
	{
		ID: "msg-2026-07-28-self-custody-matters-en", Channel: "news", Language: "en",
		Title:  "Why self-custody matters more than ever",
		Slug:   "why-self-custody-matters-more-than-ever",
		Source: "https://trezor.io/blog/news/why-self-custody-matters-more-than-ever",
		Author: "Henry Windle", Published: "2026-07-28T09:00:00Z",
		Summary:     "Exchange failures keep proving the same lesson: if you don't control the keys, you don't control the coins. Why self-custody is the only guarantee, and how a hardware wallet makes it practical.",
		ContentText: "Exchange failures keep proving the same lesson: if you don't control the keys, you don't control the coins. Why self-custody is the only guarantee, and how a hardware wallet makes it practical.",
		ContentHTML: "<p>Exchange failures keep proving the same lesson: if you don't control the keys, you don't control the coins. Why self-custody is the only guarantee, and how a hardware wallet makes it practical.</p>",
		Image:       "selfcustody26.jpg",
		Tags:        []string{"self-custody", "bitcoin", "en"},
	},
	{
		ID: "msg-2026-07-28-self-custody-matters-cs", Channel: "news", Language: "cs",
		Title:  "Proč je self-custody důležitější než kdy dřív",
		Slug:   "proc-je-self-custody-dulezitejsi-nez-kdy-driv",
		Source: "https://trezor.io/cs/blog/news/why-self-custody-matters-more-than-ever",
		Author: "Henry Windle", Published: "2026-07-28T09:00:00Z",
		Summary:     "Selhání burz stále dokazují jedno: pokud nekontrolujete klíče, nekontrolujete mince. Proč je self-custody jedinou zárukou — a jak ji hardwarová peněženka dělá praktickou.",
		ContentText: "Selhání burz stále dokazují jedno: pokud nekontrolujete klíče, nekontrolujete mince. Proč je self-custody jedinou zárukou — a jak ji hardwarová peněženka dělá praktickou.",
		ContentHTML: "<p>Selhání burz stále dokazují jedno: pokud nekontrolujete klíče, nekontrolujete mince. Proč je self-custody jedinou zárukou — a jak ji hardwarová peněženka dělá praktickou.</p>",
		Image:       "selfcustody26.jpg",
		Tags:        []string{"self-custody", "bitcoin", "cs"},
	},
	{
		ID: "msg-2026-08-05-coldcard", Channel: "security", Language: "en",
		Title:  "Coldcard vulnerability: Trezor devices are not affected",
		Slug:   "coldcard-vulnerability-trezor-devices-are-not-affected",
		Source: "https://trezor.io/blog/news/coldcard-vulnerability-trezor-devices-are-not-affected",
		Author: "Trezor Team", Published: "2026-08-05T16:00:00Z",
		Summary:     "Following the recent Coldcard disclosure: Trezor hardware wallets are not affected. Here is what the vulnerability was, why it does not apply to our devices, and what we still recommend.",
		ContentText: "Following the recent Coldcard disclosure: Trezor hardware wallets are not affected. Here is what the vulnerability was, why it does not apply to our devices, and what we still recommend.",
		ContentHTML: "<p>Following the recent Coldcard disclosure: Trezor hardware wallets are not affected. Here is what the vulnerability was, why it does not apply to our devices, and what we still recommend.</p>",
		Image:       "coldcard.jpg",
		Tags:        []string{"security", "en"},
	},
	{
		ID: "msg-2026-08-06-passphrase-faq", Channel: "security", Language: "en",
		Title:  "Passphrase FAQ: What every passphrase user should know",
		Slug:   "passphrase-faq-what-every-passphrase-user-should-know",
		Source: "https://trezor.io/blog/security/passphrase-faq-what-every-passphrase-user-should-know",
		Author: "Trezor Team", Published: "2026-08-06T09:00:00Z",
		Summary:     "The passphrase is a sharp tool. Used carefully, it adds real security to your wallet; used carelessly, it is one of the fastest ways to lose access to your own crypto. This FAQ covers what a passphrase is, whether you need one, and how to choose, write down and test one.",
		ContentText: "The passphrase is a sharp tool. Used carefully, it adds real security to your wallet. Used carelessly, it's one of the fastest ways to lose access to your own crypto. Before anything else: passphrases cannot be changed or removed — if you lose yours, you lose access to the funds in that wallet. Write it down, store it safely, test it before you send anything significant.",
		ContentHTML: "<p>The passphrase is a sharp tool. Used carefully, it adds real security to your wallet. Used carelessly, it's one of the fastest ways to lose access to your own crypto.</p><p><strong>Passphrases cannot be changed or removed.</strong> If you lose yours, you lose access to the funds in that wallet. Write it down, store it safely, test it before you send anything significant.</p>",
		Image:       "passphrase.jpg",
		Tags:        []string{"security", "self-custody", "en"},
	},
}

// privateItem is the demo item for the capability (per-order) tracking feed.
var privateItem = demoItem{
	ID:          "order-2026-0841-shipped",
	Channel:     "tracking",
	Language:    "en",
	Title:       "Your Trezor order #2026-0841 has shipped",
	Slug:        "order-2026-0841",
	Author:      "Trezor Team",
	Published:   "2026-09-01T08:12:00Z",
	Summary:     "Your Trezor Safe 5 is on its way. Track the parcel with carrier ID DHL-8491-2203-77; estimated delivery is 3–5 business days.",
	ContentText: "Your Trezor Safe 5 (order #2026-0841) is on its way.\n\nCarrier: DHL Express\nTracking number: DHL-8491-2203-77\nEstimated delivery: 3–5 business days\n\nThis is a signed capability feed: only you (with this unguessable URL) can read it, and it expires automatically after the delivery window.",
	ContentHTML: "<p>Your Trezor Safe 5 (order #2026-0841) is on its way.</p><ul><li>Carrier: DHL Express</li><li>Tracking number: DHL-8491-2203-77</li><li>Estimated delivery: 3–5 business days</li></ul><p>This is a signed capability feed: only you (with this unguessable URL) can read it, and it expires automatically after the delivery window.</p>",
	Image:       "logo.png",
	Tags:        []string{"delivery", "trezor-safe-5", "en"},
}
