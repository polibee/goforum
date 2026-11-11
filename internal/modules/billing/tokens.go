package billing

import (
	"sort"
	"strings"
)

// TokenOption is one selectable token for the Deipay checkout. Admins pick
// these from a list instead of typing "chainID:contract" by hand.
type TokenOption struct {
	// ID is the value persisted into billing.deipay.tokens.
	ID       string
	Symbol   string
	Name     string
	ChainID  int64
	Chain    string
	Address  string
	Decimals int
	// Popular marks tokens shown first for quick onboarding.
	Popular bool
}

// TokenCatalog is the built-in token list. Addresses are the canonical
// mainnet contracts so operators never have to look them up.
var TokenCatalog = []TokenOption{
	// --- Ethereum (chain 1) ---
	{Symbol: "USDT", Name: "Tether USD", ChainID: 1, Chain: "Ethereum", Address: "0xdac17f958d2ee523a2206206994597c13d831ec7", Decimals: 6, Popular: true},
	{Symbol: "USDC", Name: "USD Coin", ChainID: 1, Chain: "Ethereum", Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", Decimals: 6, Popular: true},
	{Symbol: "DAI", Name: "Dai Stablecoin", ChainID: 1, Chain: "Ethereum", Address: "0x6b175474e89094c44da98b954eedeac495271d0f", Decimals: 18, Popular: true},
	{Symbol: "USDD", Name: "Decentralized USD", ChainID: 1, Chain: "Ethereum", Address: "0x0c10bf8fcb7bf54198cc2b6c08b1c00110a6781c", Decimals: 18},
	{Symbol: "TUSD", Name: "TrueUSD", ChainID: 1, Chain: "Ethereum", Address: "0x0000000000085d4780b73119b644ae5ecd22b376", Decimals: 18},
	{Symbol: "WETH", Name: "Wrapped Ether", ChainID: 1, Chain: "Ethereum", Address: "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2", Decimals: 18},

	// --- BNB Smart Chain (chain 56) ---
	{Symbol: "USDT", Name: "Tether USD (BSC)", ChainID: 56, Chain: "BNB Chain", Address: "0x55d398326f99059ff775485246999027b3197955", Decimals: 18, Popular: true},
	{Symbol: "USDC", Name: "USD Coin (BSC)", ChainID: 56, Chain: "BNB Chain", Address: "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d", Decimals: 18},
	{Symbol: "BUSD", Name: "Binance USD", ChainID: 56, Chain: "BNB Chain", Address: "0xe9e7cea3dedca5984780bafc599bd69add087d56", Decimals: 18},

	// --- Polygon (chain 137) ---
	{Symbol: "USDT", Name: "Tether USD (Polygon)", ChainID: 137, Chain: "Polygon", Address: "0xc2132d05d31c914a87c6611c10748aeb04b58e8f", Decimals: 6, Popular: true},
	{Symbol: "USDC", Name: "USD Coin (Polygon)", ChainID: 137, Chain: "Polygon", Address: "0x3c499c542cef5e3811e1192ce70d8cc03d5c3359", Decimals: 6},

	// --- Arbitrum One (chain 42161) ---
	{Symbol: "USDT", Name: "Tether USD (Arbitrum)", ChainID: 42161, Chain: "Arbitrum", Address: "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9", Decimals: 6},
	{Symbol: "USDC", Name: "USD Coin (Arbitrum)", ChainID: 42161, Chain: "Arbitrum", Address: "0xaf88d065e77c8cc2239327c5edb3a432268e5831", Decimals: 6},

	// --- Base (chain 8453) ---
	{Symbol: "USDC", Name: "USD Coin (Base)", ChainID: 8453, Chain: "Base", Address: "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913", Decimals: 6},
	{Symbol: "DAI", Name: "Dai Stablecoin (Base)", ChainID: 8453, Chain: "Base", Address: "0x50c5725949a6f0c72e6c4a641f24049a917db0cb", Decimals: 18},
}

// tokenID builds the persisted "chainId:address" form.
func tokenKey(chainID int64, address string) string {
	return itoa64(chainID) + ":" + strings.ToLower(strings.TrimSpace(address))
}

func init() {
	for i := range TokenCatalog {
		TokenCatalog[i].ID = tokenKey(TokenCatalog[i].ChainID, TokenCatalog[i].Address)
	}
	sort.SliceStable(TokenCatalog, func(i, j int) bool {
		if TokenCatalog[i].Popular != TokenCatalog[j].Popular {
			return TokenCatalog[i].Popular
		}
		if TokenCatalog[i].ChainID != TokenCatalog[j].ChainID {
			return TokenCatalog[i].ChainID < TokenCatalog[j].ChainID
		}
		return TokenCatalog[i].Symbol < TokenCatalog[j].Symbol
	})
}

// TokenByID looks up a catalog entry by its persisted key.
func TokenByID(id string) (TokenOption, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, t := range TokenCatalog {
		if strings.ToLower(t.ID) == id {
			return t, true
		}
	}
	return TokenOption{}, false
}

// ParseTokenList splits the stored setting into normalized entries.
func ParseTokenList(raw string) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// SelectedTokens returns the configured tokens with catalog metadata attached.
// Entries that are not in the catalog still appear so custom contracts keep
// working, but they are flagged as custom.
func SelectedTokens(raw string) []TokenOption {
	ids := ParseTokenList(raw)
	out := make([]TokenOption, 0, len(ids))
	for _, id := range ids {
		if t, ok := TokenByID(id); ok {
			out = append(out, t)
			continue
		}
		opt := TokenOption{ID: id, Symbol: "自定义", Name: id}
		if chain, addr, ok := strings.Cut(id, ":"); ok {
			opt.Chain = "Chain " + chain
			opt.Address = addr
		}
		out = append(out, opt)
	}
	return out
}

// ChainGroups groups the catalog by chain for the picker UI.
type ChainGroup struct {
	ChainID int64
	Chain   string
	Tokens  []TokenOption
}

// TokenGroups returns catalog tokens grouped by chain, popular chains first.
func TokenGroups() []ChainGroup {
	seen := map[int64]int{}
	groups := make([]ChainGroup, 0, 6)
	for _, t := range TokenCatalog {
		if idx, ok := seen[t.ChainID]; ok {
			groups[idx].Tokens = append(groups[idx].Tokens, t)
			continue
		}
		seen[t.ChainID] = len(groups)
		groups = append(groups, ChainGroup{ChainID: t.ChainID, Chain: t.Chain, Tokens: []TokenOption{t}})
	}
	return groups
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
