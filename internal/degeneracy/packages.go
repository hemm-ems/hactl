package degeneracy

// WirePackages are the packages that decode Home Assistant or companion
// payloads with encoding/json directly. Two gates quantify over this list and
// deliberately share it so a package can never sit between them:
//
//   - sweep_test.go (H-14) derives every json.Unmarshal site inside these
//     packages and forces each to call Check or carry a written reason.
//   - surfaceaudit.DecodeSurface (H-7) derives every decode site the sweep
//     cannot see — yaml unmarshals, decoder constructions, websocket ReadJSON,
//     and any json decode *outside* these packages — and requires each to be
//     dispositioned in dev/surfaces/decode.manifest.
//
// Adding a package here moves its json decodes under the sweep; leaving it out
// leaves them on the decode surface. There is no third place for them to be.
//
// internal/cache, internal/config and friends are absent on purpose: they
// decode only hactl's own on-disk files, whose shape hactl also writes — and
// because they are absent, any wire decode they ever grow lands on the decode
// surface rather than nowhere.
var WirePackages = []string{
	"internal/haapi",
	"internal/companion",
	"internal/cmd",
	"internal/analyze",
}
