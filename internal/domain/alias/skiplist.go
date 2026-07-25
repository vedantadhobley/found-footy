// skiplist.go — multilingual skip-list used by the selection pipeline
// to drop pure organizational/generic terms while preserving team-
// identifying words that look generic but aren't.
//
// Design ref: docs/design/proposals/team-aliases.md § "Word-
// processing pipeline". Corrected mid-design (2026-07-19) after
// finding that an earlier draft would have wrongly skipped
// "sporting" (Sporting CP) and — during the eval — validated via
// data probes that words like `united`, `city`, `athletic`, `real`,
// `sporting`, `borussia`, `juventus`, `elftal`, `mannschaft`,
// `selecao` DO identify specific teams and must be kept.
package alias

// skipList contains lowercased tokens that never carry team identity
// on their own. Pure organizational descriptors + articles + generic
// football-terminology across multiple languages.
//
// NOT in this list — deliberately: `united, city, athletic, sporting,
// real, rangers, rovers, town, wanderers, borussia, juventus, dynamo,
// olympique, olympic, ajax` and foreign-language nickname roots like
// `selecao, seleccion, elftal, mannschaft, azzurri, oranje, bleus`.
// These DO identify specific teams and Python's LLM was known to
// over-filter them — we don't.
var skipList = map[string]struct{}{
	// Suffixes on club names.
	"fc": {}, "ac": {}, "sc": {}, "cf": {},

	// Football-organizational terms across languages.
	"football": {}, "futbol": {}, "futebol": {}, "calcio": {},
	"fussball": {}, // ß→ss preprocessing means fußball → fussball
	"soccer":   {},
	"club":     {}, "clube": {}, "klub": {},
	"association": {}, "associazione": {}, "sociedade": {}, "society": {},
	"sad":         {}, // Sociedad Anónima Deportiva (Spanish corporate suffix)
	"sport":       {}, "sports": {},
	"deportivo":   {},

	// Team-shape generics — apply carefully. "nazionale" alone is
	// generic ("national team" in Italian) but appears standalone on
	// national-team labels regardless of country. Same for
	// "nationalmannschaft" (German compound).
	"nazionale":          {},
	"nationalmannschaft": {},
	"selection":          {},
	"selectionnee":       {},
	"national":           {},
	"team":               {},
	"equipe":             {},
	"reprezentacja":      {},
	"reprezentanca":      {},
	"reprezentacije":     {},
	"reprezentacji":      {},
	"nogometna":          {}, "nogometne": {},
	"futbolowa":  {}, "futbolista": {},
	"voetbal":    {}, "voetbalelftal": {},
	"nacional":   {}, // Portuguese/Spanish generic "national"
	"seleccion":  {}, // Spanish generic "selection/team" — appears across many teams
	"seleccio":   {}, // Catalan
	"selecc":     {}, // truncation artifact

	// Articles + connectives across languages.
	"the":  {}, "los": {}, "las": {}, "la": {}, "le": {}, "les": {},
	"el":   {}, "il": {}, "das": {}, "der": {}, "die": {}, "den": {},
	"de":   {}, "del": {}, "du": {}, "di": {}, "da": {}, "do": {}, "dos": {},
	"and":  {}, "of": {}, "van": {}, "en": {}, "y": {},

	// Genuinely-multi-language generic words observed leaking the
	// ≥2-lang threshold in the 2026-07-23 real-pipeline audit. These
	// tokens truly appear in aliases across multiple language variants
	// (unlike the multi-word verbatim-copy case which is handled by
	// the pipeline fix in select.go extractSources) — but they're
	// generic enough that they'd match huge amounts of unrelated
	// Twitter content:
	"casa":     {}, // Spanish "house" — Real Madrid; also in Portuguese/Italian
	"royal":    {}, // English "royal" — Real Madrid; appears in labels of multiple langs
	"company":  {}, // English generic — Bayer Leverkusen ("The Company XI") + label carriers
	"mecanica": {}, // Spanish "mechanical" — Netherlands; likely a P1449 nickname artifact

	// Placeholder junk from tokenization artifacts.
	"mens": {}, // from splitting "X men's national football team" → mens

	// Foreign country-name variants that duplicate the English canonical
	// name and don't match English tweets (identified by extension test,
	// 2026-07-19). Only include forms that are clearly cross-team noise
	// — team-specific nicknames stay out of this list.
	"holanda":     {}, "holandesa": {},
	"croacia":     {}, "croata": {},
	"alemana":     {}, "alemania": {}, "germania": {},
	"inglaterra":  {}, "inglesa": {},
	"belga":       {}, "belgica": {}, "belgische": {}, "belghe": {},
	"espanola":    {},
	"francesa":    {}, "francia": {},
	"mexic":       {}, "mexicana": {},
	"estados":     {}, "unidos": {},
	"baixos":      {}, "paises": {},
	"occidental":  {}, // "West Germany" artifact
}

// isSkipped reports whether tok is in the multilingual skip-list.
// Callers pass already-lowercased + diacritic-stripped tokens.
func isSkipped(tok string) bool {
	_, ok := skipList[tok]
	return ok
}
