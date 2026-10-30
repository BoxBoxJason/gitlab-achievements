package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	pathpkg "path"
	"strings"
	"sync"
)

// Tier badges.
//
// Every tier of a criteria shares one piece of art, and GitLab only names
// the tier in a tooltip, so a profile gave no way to tell Committer II from
// Committer IX without hovering it. Each tier's avatar is therefore that
// art with a marker in its bottom-right corner: the marker's colour is the
// tier's band (bronze, silver, gold, purple, three tiers each) and the
// number of pips in it is the tier within its band. Committer VIII is gold,
// two pips.
//
// A numeral was tried first. GitLab draws an avatar at 48 CSS pixels, one
// and a half per art pixel, and a Roman numeral that small blurs into a
// smudge. A colour and a count of at most three both survive it.
//
// The marker is drawn here, over the embedded art, rather than baked into
// eleven PNGs per criteria, so a criteria still ships with one image and
// gets its tiers' badges for free.

const (
	// artGrid is the grid the art is authored on (see assets/gen.py). The
	// marker is laid out on it too and scaled with it, so it lands on the
	// same pixel boundaries as the art whatever size the file is.
	artGrid = 32
	// pipSize is one pip's side, in art pixels, its keyline included.
	// Neighbouring pips share a keyline column.
	pipSize = 6
	// pipFace is the side of a pip's face, inside its keyline.
	pipFace = pipSize - 2
	// pipsPerBand is how many tiers a band spans, and so the most pips a
	// marker ever shows.
	pipsPerBand = 3
)

// tierBand is one band's pair of colours: the pip's face, and the shade on
// its bottom row that gives it the same lighting as the art.
type tierBand struct {
	fill  color.NRGBA
	shade color.NRGBA
}

//nolint:gochecknoglobals,mnd // a fixed palette, read-only; the channels are the palette's data, not constants
var (
	// tierBands runs from the lowest band up. web/src/components/TierPips.vue
	// colours the frontend's tier rows with the same values, so keep the
	// two in step.
	tierBands = [...]tierBand{
		{fill: color.NRGBA{R: 205, G: 127, B: 50, A: 255}, shade: color.NRGBA{R: 150, G: 86, B: 30, A: 255}},    // bronze
		{fill: color.NRGBA{R: 200, G: 206, B: 216, A: 255}, shade: color.NRGBA{R: 140, G: 148, B: 162, A: 255}}, // silver
		{fill: color.NRGBA{R: 255, G: 200, B: 40, A: 255}, shade: color.NRGBA{R: 200, G: 140, B: 0, A: 255}},    // gold
		{fill: color.NRGBA{R: 190, G: 80, B: 255, A: 255}, shade: color.NRGBA{R: 120, G: 30, B: 190, A: 255}},   // purple
	}

	// keyline is the art's own outline colour (PALETTE['K'] in gen.py), so
	// the marker reads as part of the icon rather than a sticker on it.
	keyline = color.NRGBA{R: 27, G: 27, B: 31, A: 255}
)

// MaxBadgedTier is the highest tier a badge can tell apart from the one
// below it. Past it the top band would need a fourth pip.
const MaxBadgedTier = int64(len(tierBands) * pipsPerBand)

// tierMarker returns the band a tier is drawn in and how many pips it
// shows. Tiers number from 1.
func tierMarker(tier int64) (tierBand, int) {
	index := int(max(tier, 1) - 1)
	band := min(index/pipsPerBand, len(tierBands)-1)

	return tierBands[band], index - band*pipsPerBand + 1
}

// Avatar is one tier's badge, ready to upload.
type Avatar struct {
	// Key names this exact image: the asset path plus a digest of PNG, so
	// it changes whenever the art, the tier, or the marker's design does.
	// Recording it is how bootstrap knows which image GitLab was last
	// given, since GitLab hands back an avatar's URL but never its bytes.
	Key string
	// FileName is what to call the upload. It carries the same digest, so
	// a new image is stored under a new name, and so a new URL, rather
	// than replacing the old one at an address browsers have cached.
	FileName string
	// PNG is the image itself.
	PNG []byte
}

//nolint:gochecknoglobals // a memo of pure renders of compiled-in assets
var badges sync.Map // badgeID -> Avatar

type badgeID struct {
	path string
	tier int64
}

// renderBadge returns the badge for one tier of the art at path, rendering
// it on first use. Bootstrap re-checks every achievement hourly, and the
// inputs are compiled in, so there is nothing to gain from drawing the same
// 330 images again each time.
func renderBadge(path string, tier int64) (Avatar, error) {
	memoKey := badgeID{path: path, tier: tier}
	if cached, ok := badges.Load(memoKey); ok {
		return cached.(Avatar), nil //nolint:forcetypeassert // only Avatar values are ever stored
	}

	file, err := OpenAvatar(path)
	if err != nil {
		return Avatar{}, err
	}
	defer func() { _ = file.Close() }()

	art, err := io.ReadAll(file)
	if err != nil {
		return Avatar{}, fmt.Errorf("failed to read avatar asset %q: %w", path, err)
	}

	data, err := drawTierMarker(art, tier)
	if err != nil {
		return Avatar{}, fmt.Errorf("avatar asset %q: %w", path, err)
	}

	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:8])
	rendered := Avatar{
		PNG:      data,
		Key:      path + "#" + digest,
		FileName: strings.TrimSuffix(pathpkg.Base(path), ".png") + "-" + digest + ".png",
	}
	badges.Store(memoKey, rendered)

	return rendered, nil
}

// drawTierMarker returns the PNG art with tier's marker drawn into its
// bottom-right corner.
//
// The art must be square and a whole multiple of the art grid, so every
// marker pixel covers a whole block of file pixels exactly as the art's own
// do. Anything else would smear the marker the way gen.py's integer scale
// exists to stop the art being smeared.
func drawTierMarker(art []byte, tier int64) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(art))
	if err != nil {
		return nil, fmt.Errorf("failed to decode: %w", err)
	}

	bounds := src.Bounds()
	if bounds.Dx() != bounds.Dy() || bounds.Dx()%artGrid != 0 {
		return nil, fmt.Errorf("is %dx%d, need a square multiple of %d to draw a tier marker on",
			bounds.Dx(), bounds.Dy(), artGrid)
	}

	img := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(img, img.Bounds(), src, bounds.Min, draw.Src)

	scale := bounds.Dx() / artGrid
	fill := func(x, y, w, h int, c color.NRGBA) {
		rect := image.Rect(x*scale, y*scale, (x+w)*scale, (y+h)*scale)
		draw.Draw(img, rect, image.NewUniform(c), image.Point{}, draw.Src)
	}

	band, pips := tierMarker(tier)
	step := pipSize - 1
	left := artGrid - (pips*step + 1)
	top := artGrid - pipSize

	for pip := range pips {
		x := left + pip*step
		fill(x, top, pipSize, pipSize, keyline)
		fill(x+1, top+1, pipFace, pipFace, band.fill)
		fill(x+1, top+pipFace, pipFace, 1, band.shade)
	}

	var out bytes.Buffer

	err = png.Encode(&out, img)
	if err != nil {
		return nil, fmt.Errorf("failed to encode: %w", err)
	}

	return out.Bytes(), nil
}
