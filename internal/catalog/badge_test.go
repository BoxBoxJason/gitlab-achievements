package catalog

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
)

func TestTierMarker(t *testing.T) {
	bronze, silver, gold, purple := tierBands[0], tierBands[1], tierBands[2], tierBands[3]

	cases := []struct {
		tier int64
		band tierBand
		pips int
	}{
		{1, bronze, 1}, {2, bronze, 2}, {3, bronze, 3},
		{4, silver, 1}, {6, silver, 3},
		{7, gold, 1}, {8, gold, 2}, {9, gold, 3},
		{10, purple, 1}, {11, purple, 2}, {12, purple, 3},
	}

	for _, c := range cases {
		band, pips := tierMarker(c.tier)
		if band != c.band || pips != c.pips {
			t.Errorf("tier %d: expected %v with %d pips, got %v with %d", c.tier, c.band, c.pips, band, pips)
		}
	}
}

func TestV1_EveryTierHasItsOwnBadge(t *testing.T) {
	for _, entry := range V1() {
		if entry.Tier > MaxBadgedTier {
			t.Errorf("%q is tier %d, but badges only tell tiers apart up to %d", entry.Name, entry.Tier, MaxBadgedTier)
		}
	}
}

func decodeBadge(t *testing.T, data []byte) image.Image {
	t.Helper()

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("failed to decode badge: %v", err)
	}

	return img
}

func readAsset(t *testing.T, path string) image.Image {
	t.Helper()

	file, err := OpenAvatar(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}

	return decodeBadge(t, data)
}

func nrgba(c color.Color) color.NRGBA {
	return color.NRGBAModel.Convert(c).(color.NRGBA) //nolint:forcetypeassert // NRGBAModel always returns NRGBA
}

func TestAvatar_DrawsTheMarkerAndLeavesTheArt(t *testing.T) {
	entry := Entry{CriteriaKey: CriteriaCommits, Tier: 8, AvatarPath: "assets/commit.png"}

	avatar, err := entry.Avatar()
	if err != nil {
		t.Fatal(err)
	}

	data := avatar.PNG

	art := readAsset(t, entry.AvatarPath)
	got := decodeBadge(t, data)

	if got.Bounds() != art.Bounds() {
		t.Fatalf("expected the badge to keep the art's %v, got %v", art.Bounds(), got.Bounds())
	}

	// Tier VIII is gold, two pips: art pixels x 21-31, y 26-31 on the
	// 32 grid, which commit.png draws at 3x.
	const scale = 3

	at := func(x, y int) color.NRGBA { return nrgba(got.At(x*scale+1, y*scale+1)) }
	gold := tierBands[2]

	for _, check := range []struct {
		name string
		x, y int
		want color.NRGBA
	}{
		{"first pip's face", 22, 27, gold.fill},
		{"first pip's shade", 22, 30, gold.shade},
		{"shared keyline", 26, 28, keyline},
		{"second pip's face", 29, 28, gold.fill},
		{"corner keyline", 31, 31, keyline},
	} {
		if c := at(check.x, check.y); c != check.want {
			t.Errorf("%s at (%d,%d): expected %v, got %v", check.name, check.x, check.y, check.want, c)
		}
	}

	marker := image.Rect(21*scale, 26*scale, 32*scale, 32*scale)
	bounds := art.Bounds()

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (image.Point{X: x, Y: y}).In(marker) {
				continue
			}

			if nrgba(got.At(x, y)) != nrgba(art.At(x, y)) {
				t.Fatalf("expected the art untouched outside the marker, (%d,%d) changed", x, y)
			}
		}
	}
}

func TestAvatar_ScalesTheMarkerWithTheArt(t *testing.T) {
	// owl.png is hand-drawn at 64px rather than the generated 96, so the
	// marker has to land on its 2x grid instead.
	entry := Entry{CriteriaKey: CriteriaNightOwlDays, Tier: 1, AvatarPath: "assets/owl.png"}

	avatar, err := entry.Avatar()
	if err != nil {
		t.Fatal(err)
	}

	data := avatar.PNG

	got := decodeBadge(t, data)
	if got.Bounds().Dx() != 64 {
		t.Fatalf("expected the owl's badge to stay 64px, got %v", got.Bounds())
	}

	if c := nrgba(got.At(31*2, 31*2)); c != keyline {
		t.Errorf("expected the marker's corner keyline at the owl's scale, got %v", c)
	}

	if c := nrgba(got.At(28*2, 28*2)); c != tierBands[0].fill {
		t.Errorf("expected a bronze pip at the owl's scale, got %v", c)
	}
}

func TestAvatarKey_NamesEachTiersImage(t *testing.T) {
	keys := make(map[string]int64)

	for tier := int64(1); tier <= MaxBadgedTier; tier++ {
		entry := Entry{CriteriaKey: CriteriaCommits, Tier: tier, AvatarPath: "assets/commit.png"}

		key, err := entry.AvatarKey()
		if err != nil {
			t.Fatal(err)
		}

		// The bare path is what rows recorded before tiers had badges of
		// their own. A key equal to it would leave every one of those
		// achievements showing the unmarked art.
		if key == entry.AvatarPath || !strings.HasPrefix(key, entry.AvatarPath+"#") {
			t.Errorf("tier %d: expected a key extending %q, got %q", tier, entry.AvatarPath, key)
		}

		if other, ok := keys[key]; ok {
			t.Errorf("tiers %d and %d share the key %q", other, tier, key)
		}

		keys[key] = tier

		again, err := entry.AvatarKey()
		if err != nil || again != key {
			t.Errorf("tier %d: expected a stable key %q, got %q (%v)", tier, key, again, err)
		}
	}
}

func TestAvatar_FileNameFollowsTheImage(t *testing.T) {
	first, err := Entry{CriteriaKey: CriteriaCommits, Tier: 1, AvatarPath: "assets/commit.png"}.Avatar()
	if err != nil {
		t.Fatal(err)
	}

	second, err := Entry{CriteriaKey: CriteriaCommits, Tier: 2, AvatarPath: "assets/commit.png"}.Avatar()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(first.FileName, "commit-") || !strings.HasSuffix(first.FileName, ".png") {
		t.Errorf("expected a commit-<digest>.png upload name, got %q", first.FileName)
	}

	if first.FileName == second.FileName {
		t.Errorf("expected two different images to upload under different names, both are %q", first.FileName)
	}
}

func TestAvatarKey_EmptyWithoutAvatar(t *testing.T) {
	key, err := Entry{CriteriaKey: CriteriaCommits, Tier: 1}.AvatarKey()
	if err != nil || key != "" {
		t.Errorf("expected no key and no error, got %q (%v)", key, err)
	}
}

func TestDrawTierMarker_RejectsArtOffTheGrid(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 40, 40))); err != nil {
		t.Fatal(err)
	}

	if _, err := drawTierMarker(buf.Bytes(), 1); err == nil {
		t.Error("expected 40x40 art to be refused, it is no multiple of the 32 grid")
	}
}
