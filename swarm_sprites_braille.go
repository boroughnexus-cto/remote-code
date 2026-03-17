package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ─── Braille sprite engine ─────────────────────────────────────────────────
//
// Each Unicode braille character is a 2×4 dot grid, giving a brailleFrame
// [16][12] a 12×16 pixel canvas that renders into 6×4 terminal cells — the
// same footprint as a regular spriteFrame but with 4× the pixel count.
//
// Braille dot layout (Unicode standard):
//
//	col 0  col 1
//	dot 1  dot 4   → bits 0x01  0x08   ← row 0
//	dot 2  dot 5   → bits 0x02  0x10   ← row 1
//	dot 3  dot 6   → bits 0x04  0x20   ← row 2
//	dot 7  dot 8   → bits 0x40  0x80   ← row 3
//
// renderBraille:     4 rows × 6 chars  (12×16 pixels compressed)
// renderBrailleTall: 8 rows × 12 chars (12×16 pixels expanded with ▀ trick, ~square pixels)

type brailleFrame [16][12]lipgloss.Color

// brailleBit[row][col] is the Unicode bit for that dot position in the 2×4 grid.
var brailleBit = [4][2]rune{
	{0x01, 0x08}, // row 0: dots 1, 4
	{0x02, 0x10}, // row 1: dots 2, 5
	{0x04, 0x20}, // row 2: dots 3, 6
	{0x40, 0x80}, // row 3: dots 7, 8
}

// ─── Rendering ────────────────────────────────────────────────────────────────

// renderBraille produces 4 terminal lines × 6 chars using braille characters.
// Each terminal cell holds a 2×4 pixel block; fg/bg are picked to preserve detail.
func renderBraille(f brailleFrame, bg lipgloss.Color) string {
	rows := make([]string, 4)
	for by := 0; by < 4; by++ {
		var sb strings.Builder
		for bx := 0; bx < 6; bx++ {
			px, py := bx*2, by*4
			var pix [4][2]lipgloss.Color
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					c := f[py+dy][px+dx]
					if c == spBG {
						c = bg
					}
					pix[dy][dx] = c
				}
			}
			fgC, bgC := brailleCellColors(pix)
			if fgC == bgC {
				sb.WriteString(lipgloss.NewStyle().Background(bgC).Render(" "))
				continue
			}
			var bits rune
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if pix[dy][dx] == fgC {
						bits |= brailleBit[dy][dx]
					}
				}
			}
			ch := '\u2800' + bits
			sb.WriteString(
				lipgloss.NewStyle().Foreground(fgC).Background(bgC).Render(string(ch)),
			)
		}
		rows[by] = sb.String()
	}
	return strings.Join(rows, "\n")
}

// renderBrailleTall produces 8 terminal lines × 12 chars using the ▀ half-block
// trick on the 12-column pixel grid. Each terminal cell covers 1 pixel column and
// 2 pixel rows. Result: same width and double the height of renderBraille, giving
// a portrait that looks roughly square with typical terminal font aspect ratios.
func renderBrailleTall(f brailleFrame, bg lipgloss.Color) string {
	rows := make([]string, 8)
	for row := 0; row < 8; row++ {
		var sb strings.Builder
		y0, y1 := row*2, row*2+1
		for x := 0; x < 12; x++ {
			top, bot := f[y0][x], f[y1][x]
			if top == spBG {
				top = bg
			}
			if bot == spBG {
				bot = bg
			}
			sb.WriteString(
				lipgloss.NewStyle().Foreground(top).Background(bot).Render("▀"),
			)
		}
		rows[row] = sb.String()
	}
	return strings.Join(rows, "\n")
}

// renderBrailleHead produces one terminal line (top braille row) for sidebar use.
func renderBrailleHead(f brailleFrame, bg lipgloss.Color) string {
	// Render top 4 pixel rows (first braille row)
	var sb strings.Builder
	for bx := 0; bx < 6; bx++ {
		px := bx * 2
		var pix [4][2]lipgloss.Color
		for dy := 0; dy < 4; dy++ {
			for dx := 0; dx < 2; dx++ {
				c := f[dy][px+dx]
				if c == spBG {
					c = bg
				}
				pix[dy][dx] = c
			}
		}
		fgC, bgC := brailleCellColors(pix)
		if fgC == bgC {
			sb.WriteString(lipgloss.NewStyle().Background(bgC).Render(" "))
			continue
		}
		var bits rune
		for dy := 0; dy < 4; dy++ {
			for dx := 0; dx < 2; dx++ {
				if pix[dy][dx] == fgC {
					bits |= brailleBit[dy][dx]
				}
			}
		}
		ch := '\u2800' + bits
		sb.WriteString(
			lipgloss.NewStyle().Foreground(fgC).Background(bgC).Render(string(ch)),
		)
	}
	return sb.String()
}

// brailleCellColors picks fg (minority/accent) and bg (majority/fill) for a 2×4 cell.
func brailleCellColors(pix [4][2]lipgloss.Color) (fg, bg lipgloss.Color) {
	counts := make(map[lipgloss.Color]int, 8)
	for dy := 0; dy < 4; dy++ {
		for dx := 0; dx < 2; dx++ {
			counts[pix[dy][dx]]++
		}
	}
	var top1, top2 lipgloss.Color
	var cnt1, cnt2 int
	for c, n := range counts {
		if n > cnt1 {
			top2, cnt2 = top1, cnt1
			top1, cnt1 = c, n
		} else if n > cnt2 {
			top2, cnt2 = c, n
		}
	}
	if cnt2 == 0 {
		return top1, top1 // solid cell
	}
	// bg = majority, fg = minority (braille dots mark the accent pixels)
	return top2, top1
}

// scaleToBraille upscales a 6×8 spriteFrame to 12×16 by doubling each pixel.
// Use this for roles without a custom braille design.
func scaleToBraille(f spriteFrame) brailleFrame {
	var bf brailleFrame
	for y := 0; y < 8; y++ {
		for x := 0; x < 6; x++ {
			c := f[y][x]
			bf[y*2][x*2] = c
			bf[y*2][x*2+1] = c
			bf[y*2+1][x*2] = c
			bf[y*2+1][x*2+1] = c
		}
	}
	return bf
}

// ─── Row helper ───────────────────────────────────────────────────────────────

func row12(a, b, c, d, e, f, g, h, i, j, k, l lipgloss.Color) [12]lipgloss.Color {
	return [12]lipgloss.Color{a, b, c, d, e, f, g, h, i, j, k, l}
}

// ─── 12×16 Sprite definitions ─────────────────────────────────────────────────
//
// Shared layout:
//   rows  0–3 : hair / hat / crown
//   rows  4–11: face (4=top border, 5=forehead, 6-8=eyes, 9-10=mouth, 11=chin)
//   rows 12–15: body / legs

var (
	bK = spK // outline / dark
	bS = spS // skin
	bE = spE // eye dot
	bH = spH // neutral hair
	bP = spP // pink  (orchestrator)
	bT = spT // teal  (senior-dev)
	bY = spY // yellow (qa-agent)
	bB = spB // blue  (devops)
	bV = spV // violet (researcher)
	bG = spG // slate (worker)
	bX = spBG
)

// ── Orchestrator (pink crown, three-spike tiara) ──────────────────────────────
var brailleOrchBase = brailleFrame{
	row12(bX, bP, bX, bP, bX, bX, bX, bP, bX, bP, bX, bX), // 0 spike tips
	row12(bP, bP, bX, bP, bP, bX, bX, bP, bP, bX, bP, bP), // 1 spike bases
	row12(bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP), // 2 crown band
	row12(bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP), // 3 crown band
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 6 eye socket top
	row12(bK, bS, bK, bE, bS, bS, bS, bS, bE, bK, bS, bK), // 7 pupils
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 8 eye socket bot
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose/philtrum
	row12(bK, bS, bS, bK, bS, bS, bS, bS, bK, bS, bS, bK), // 10 mouth corners
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bP, bP, bP, bP, bP, bP, bP, bP, bK, bX), // 12 collar
	row12(bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP), // 13 body
	row12(bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP, bP), // 14 body
	row12(bX, bP, bP, bP, bX, bX, bX, bX, bP, bP, bP, bX), // 15 legs
}

var brailleOrchBlink = func() brailleFrame {
	f := brailleOrchBase
	f[7] = row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK) // closed eyes = same as socket row
	return f
}()

// ── Senior Dev (teal, dark-frame glasses, typing pose variant) ────────────────
var brailleDevBase = brailleFrame{
	row12(bX, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bX), // 0 hair top
	row12(bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT), // 1 hair body
	row12(bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT), // 2 hair body
	row12(bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT), // 3 hair base
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bK, bK, bK, bS, bS, bK, bK, bK, bS, bK), // 6 glasses frames top
	row12(bK, bS, bK, bS, bE, bK, bK, bE, bS, bK, bS, bK), // 7 lenses + pupils
	row12(bK, bS, bK, bK, bK, bS, bS, bK, bK, bK, bS, bK), // 8 glasses frames bot
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose bridge
	row12(bK, bS, bS, bK, bS, bS, bS, bS, bK, bS, bS, bK), // 10 mouth
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bT, bT, bT, bT, bT, bT, bT, bT, bK, bX), // 12 collar
	row12(bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT), // 13 body
	row12(bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT), // 14 body
	row12(bX, bT, bT, bT, bX, bX, bX, bX, bT, bT, bT, bX), // 15 legs
}

var brailleDevBlink = func() brailleFrame {
	f := brailleDevBase
	f[7] = row12(bK, bS, bK, bK, bK, bK, bK, bK, bK, bK, bS, bK)
	return f
}()

var brailleDevType = func() brailleFrame {
	f := brailleDevBase
	f[13] = row12(bT, bX, bT, bT, bT, bT, bT, bT, bT, bT, bX, bT) // arms out
	f[14] = row12(bX, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bX)
	f[15] = row12(bX, bT, bT, bT, bT, bT, bT, bT, bT, bT, bT, bX) // hands on desk
	return f
}()

// ── QA Agent (yellow, ✓ on chest) ─────────────────────────────────────────────
var brailleQABase = brailleFrame{
	row12(bX, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bX), // 0 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 1 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 2 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 3 hair base
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 6 eye sockets
	row12(bK, bS, bK, bE, bS, bS, bS, bS, bE, bK, bS, bK), // 7 pupils
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 8 eye sockets
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose
	row12(bK, bS, bK, bS, bS, bS, bS, bS, bS, bK, bS, bK), // 10 smile (wider)
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bY, bY, bY, bY, bY, bY, bY, bY, bK, bX), // 12 collar
	row12(bY, bY, bY, bY, bY, bY, bY, bY, bY, bK, bY, bY), // 13 ✓ right descender
	row12(bY, bY, bY, bY, bK, bY, bY, bK, bY, bY, bY, bY), // 14 ✓ diagonals
	row12(bX, bY, bY, bK, bY, bY, bY, bY, bY, bY, bY, bX), // 15 legs + ✓ base
}

var brailleQABlink = func() brailleFrame {
	f := brailleQABase
	f[7] = row12(bK, bS, bK, bK, bK, bK, bK, bK, bK, bK, bS, bK)
	return f
}()

// ── DevOps (blue, hard hat) ────────────────────────────────────────────────────
var brailleOpsBase = brailleFrame{
	row12(bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB), // 0 hard hat brim
	row12(bX, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bX), // 1 hat crown
	row12(bX, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bX), // 2 hat crown
	row12(bX, bX, bB, bB, bB, bB, bB, bB, bB, bB, bX, bX), // 3 hat base
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 6 eye sockets
	row12(bK, bS, bK, bE, bS, bS, bS, bS, bE, bK, bS, bK), // 7 pupils
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 8 eye sockets
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose
	row12(bK, bS, bS, bK, bS, bS, bS, bS, bK, bS, bS, bK), // 10 mouth
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bB, bB, bB, bB, bB, bB, bB, bB, bK, bX), // 12 collar
	row12(bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB), // 13 body
	row12(bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB, bB), // 14 body
	row12(bX, bB, bB, bB, bX, bX, bX, bX, bB, bB, bB, bX), // 15 legs
}

var brailleOpsBlink = func() brailleFrame {
	f := brailleOpsBase
	f[7] = row12(bK, bS, bK, bK, bK, bK, bK, bK, bK, bK, bS, bK)
	return f
}()

// ── Researcher (violet, mortarboard + violet glasses) ─────────────────────────
var brailleResBase = brailleFrame{
	row12(bK, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bK), // 0 mortarboard flat top
	row12(bX, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bX), // 1 cap body
	row12(bX, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bX), // 2 cap body
	row12(bX, bX, bV, bV, bV, bV, bV, bV, bV, bV, bX, bX), // 3 cap base band
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bV, bV, bV, bS, bS, bV, bV, bV, bS, bK), // 6 violet frames top
	row12(bK, bS, bV, bS, bE, bV, bV, bE, bS, bV, bS, bK), // 7 lenses + pupils
	row12(bK, bS, bV, bV, bV, bS, bS, bV, bV, bV, bS, bK), // 8 violet frames bot
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose bridge
	row12(bK, bS, bS, bK, bS, bS, bS, bS, bK, bS, bS, bK), // 10 mouth
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bV, bV, bV, bV, bV, bV, bV, bV, bK, bX), // 12 collar
	row12(bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV), // 13 body
	row12(bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV, bV), // 14 body
	row12(bX, bV, bV, bV, bX, bX, bX, bX, bV, bV, bV, bX), // 15 legs
}

var brailleResBlink = func() brailleFrame {
	f := brailleResBase
	f[7] = row12(bK, bS, bV, bV, bV, bV, bV, bV, bV, bV, bS, bK)
	return f
}()

// ── Worker (gray, plain) ───────────────────────────────────────────────────────
var brailleWrkBase = brailleFrame{
	row12(bX, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bX), // 0 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 1 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 2 hair
	row12(bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH, bH), // 3 hair
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 4 face top
	row12(bK, bS, bS, bS, bS, bS, bS, bS, bS, bS, bS, bK), // 5 forehead
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 6 eye sockets
	row12(bK, bS, bK, bE, bS, bS, bS, bS, bE, bK, bS, bK), // 7 pupils
	row12(bK, bS, bK, bK, bS, bS, bS, bS, bK, bK, bS, bK), // 8 eye sockets
	row12(bK, bS, bS, bS, bS, bK, bK, bS, bS, bS, bS, bK), // 9 nose
	row12(bK, bS, bS, bK, bS, bS, bS, bS, bK, bS, bS, bK), // 10 mouth
	row12(bK, bK, bS, bS, bS, bS, bS, bS, bS, bS, bK, bK), // 11 chin
	row12(bX, bK, bG, bG, bG, bG, bG, bG, bG, bG, bK, bX), // 12 collar
	row12(bG, bG, bG, bG, bG, bG, bG, bG, bG, bG, bG, bG), // 13 body
	row12(bG, bG, bG, bG, bG, bG, bG, bG, bG, bG, bG, bG), // 14 body
	row12(bX, bG, bG, bG, bX, bX, bX, bX, bG, bG, bG, bX), // 15 legs
}

var brailleWrkBlink = func() brailleFrame {
	f := brailleWrkBase
	f[7] = row12(bK, bS, bK, bK, bK, bK, bK, bK, bK, bK, bS, bK)
	return f
}()

// ─── Panic/stuck (arms raised) ────────────────────────────────────────────────

func makeBraillePanic(base brailleFrame) brailleFrame {
	f := base
	body := f[13][0]
	f[12] = row12(body, bX, f[12][2], f[12][3], f[12][4], f[12][5], f[12][6], f[12][7], f[12][8], f[12][9], bX, body)
	f[13] = row12(bX, body, body, body, body, body, body, body, body, body, body, bX)
	return f
}

// ─── Public braille API ───────────────────────────────────────────────────────

// GetAgentSpriteBraille returns a 4-row × 6-char braille portrait (same terminal
// footprint as GetAgentSprite but with 12×16 pixel resolution).
func GetAgentSpriteBraille(role, status string, frame int) string {
	return renderBraille(pickBrailleFrame(role, status, frame), SpritePanelBG)
}

// GetAgentSpriteBrailleTall returns an 8-row × 12-char portrait for the detail pane,
// using the ▀ half-block trick on the 12×16 pixel grid.
func GetAgentSpriteBrailleTall(role, status string, frame int) string {
	return renderBrailleTall(pickBrailleFrame(role, status, frame), SpritePanelBG)
}

// GetAgentSpriteHeadBraille returns a single-line 6-char braille head for sidebar use.
func GetAgentSpriteHeadBraille(role, status string, frame int) string {
	return renderBrailleHead(pickBrailleFrame(role, status, frame), SpritePanelBG)
}

func pickBrailleFrame(role, status string, frame int) brailleFrame {
	blink := frame == 7

	switch status {
	case "stuck":
		base, _ := roleBrailleFrames(role)
		if frame%2 == 0 {
			return makeBraillePanic(base)
		}
		return base

	case "coding", "testing":
		if role == "senior-dev" {
			if frame%2 == 0 {
				return brailleDevType
			}
			return brailleDevBase
		}
		base, blinkF := roleBrailleFrames(role)
		if blink {
			return blinkF
		}
		return base

	default:
		base, blinkF := roleBrailleFrames(role)
		if blink {
			return blinkF
		}
		return base
	}
}

func roleBrailleFrames(role string) (base, blinkF brailleFrame) {
	switch role {
	case "orchestrator":
		return brailleOrchBase, brailleOrchBlink
	case "senior-dev":
		return brailleDevBase, brailleDevBlink
	case "qa-agent":
		return brailleQABase, brailleQABlink
	case "devops-agent":
		return brailleOpsBase, brailleOpsBlink
	case "researcher":
		return brailleResBase, brailleResBlink
	default:
		return brailleWrkBase, brailleWrkBlink
	}
}
