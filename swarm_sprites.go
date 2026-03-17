package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ─── Sprite engine ────────────────────────────────────────────────────────────
//
// Each sprite is a 6×8 pixel grid.
//
// renderSprite: half-block ▀ trick — foreground=top pixel, background=bottom pixel.
//   6×4 terminal cells (compact).
//
// renderSpriteTall: full-block mode — each pixel row = 1 terminal line, background only.
//   6×8 terminal cells (double height, more readable).
//
// Transparent pixels (spBG) are filled with the panel background colour.

const spBG = lipgloss.Color("") // transparent — caller fills with panel bg

type spriteFrame [8][6]lipgloss.Color

// renderSprite produces a 4-line string (no trailing newline) using half-block trick.
func renderSprite(f spriteFrame, bg lipgloss.Color) string {
	var rows [4]string
	for row := 0; row < 4; row++ {
		var sb strings.Builder
		y0, y1 := row*2, row*2+1
		for x := 0; x < 6; x++ {
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
	return strings.Join(rows[:], "\n")
}

// renderSpriteTall produces an 8-line string (no trailing newline).
// Uses 2 spaces per pixel (12 chars wide) so that terminal cell aspect ratio
// (~2:1 h:w) makes each pixel appear roughly square — giving a boxy portrait feel.
func renderSpriteTall(f spriteFrame, bg lipgloss.Color) string {
	rows := make([]string, 8)
	for y := 0; y < 8; y++ {
		var sb strings.Builder
		for x := 0; x < 6; x++ {
			c := f[y][x]
			if c == spBG {
				c = bg
			}
			sb.WriteString(
				lipgloss.NewStyle().Background(c).Render("  "),
			)
		}
		rows[y] = sb.String()
	}
	return strings.Join(rows, "\n")
}

// renderSpriteHead produces a single line (rows 0-1 of the pixel grid) — just
// the head, for use as a compact sidebar avatar.
func renderSpriteHead(f spriteFrame, bg lipgloss.Color) string {
	var sb strings.Builder
	for x := 0; x < 6; x++ {
		top, bot := f[0][x], f[1][x]
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
	return sb.String()
}

// ─── Palette ──────────────────────────────────────────────────────────────────

var (
	spK = lipgloss.Color("#0f172a") // outline / dark
	spS = lipgloss.Color("#fcd5a3") // skin
	spE = lipgloss.Color("#1e293b") // eye dot
	spH = lipgloss.Color("#374151") // neutral dark hair

	// Role colours (match IsoWorker / TUI theme exactly)
	spP = lipgloss.Color("#ec4899") // orchestrator  — pink
	spT = lipgloss.Color("#14b8a6") // senior-dev    — teal
	spY = lipgloss.Color("#eab308") // qa-agent      — yellow
	spB = lipgloss.Color("#3b82f6") // devops-agent  — blue
	spV = lipgloss.Color("#a855f7") // researcher    — violet
	spG = lipgloss.Color("#64748b") // worker        — slate
)

// ─── Sprite row helper ────────────────────────────────────────────────────────

func row6(a, b, c, d, e, f lipgloss.Color) [6]lipgloss.Color {
	return [6]lipgloss.Color{a, b, c, d, e, f}
}

// ─── Base body builder ───────────────────────────────────────────────────────
//
// Shared body template; callers override rows to add role personality.
//
//	y0-y1: hair / hat
//	y2-y4: face
//	y5-y7: body / legs

func bodyBase(hair, body lipgloss.Color, eyeRow [6]lipgloss.Color) spriteFrame {
	return spriteFrame{
		row6(spBG, hair, hair, hair, hair, spBG), // y0 hair top
		row6(hair, hair, hair, hair, hair, hair), // y1 hair body
		row6(spK, spS, spS, spS, spS, spK),      // y2 face upper
		eyeRow,                                   // y3 eyes
		row6(spK, spS, spS, spS, spS, spK),      // y4 face lower
		row6(spBG, body, body, body, body, spBG), // y5 shoulders
		row6(body, body, body, body, body, body), // y6 body
		row6(spBG, body, spBG, spBG, body, spBG), // y7 legs
	}
}

var eyesOpen   = row6(spK, spS, spE, spS, spE, spK)
var eyesBlink  = row6(spK, spK, spK, spK, spK, spK) // eyes closed
var eyesSquint = row6(spK, spS, spK, spS, spK, spK) // — —

// ─── Per-role sprites ─────────────────────────────────────────────────────────

// ── Orchestrator (pink crown, pink body) ─────────────────────────
// Crown: pink spikes on row 0
var spriteOrchBase = func() spriteFrame {
	f := bodyBase(spP, spP, eyesOpen)
	f[0] = row6(spP, spBG, spP, spP, spBG, spP) // crown spikes
	f[1] = row6(spP, spP, spP, spP, spP, spP)   // crown band
	return f
}()
var spriteOrchBlink = func() spriteFrame {
	f := spriteOrchBase
	f[3] = eyesBlink
	return f
}()

// ── Senior Dev (teal, dark-framed glasses) ────────────────────────
var spriteDevBase = func() spriteFrame {
	f := bodyBase(spT, spT, eyesOpen)
	f[3] = row6(spK, spK, spS, spS, spK, spK) // glasses frames
	return f
}()
var spriteDevBlink = func() spriteFrame {
	f := spriteDevBase
	f[3] = row6(spK, spK, spK, spK, spK, spK)
	return f
}()
// Typing frame: body leans slightly, hands on desk
var spriteDevType = func() spriteFrame {
	f := spriteDevBase
	f[6] = row6(spT, spT, spBG, spBG, spT, spT) // arms out typing
	f[7] = row6(spBG, spT, spT, spT, spT, spBG)
	return f
}()

// ── QA Agent (yellow, checkmark on chest) ─────────────────────────
var spriteQABase = func() spriteFrame {
	f := bodyBase(spH, spY, eyesOpen)
	f[6] = row6(spY, spY, spK, spY, spY, spY) // ✓ mark on chest
	return f
}()
var spriteQABlink = func() spriteFrame {
	f := spriteQABase
	f[3] = eyesBlink
	return f
}()

// ── DevOps (blue, hard hat) ────────────────────────────────────────
var spriteOpsBase = func() spriteFrame {
	f := bodyBase(spB, spB, eyesOpen)
	f[0] = row6(spB, spB, spB, spB, spB, spB) // hard hat brim full
	f[1] = row6(spBG, spB, spB, spB, spB, spBG) // hat rounded
	return f
}()
var spriteOpsBlink = func() spriteFrame {
	f := spriteOpsBase
	f[3] = eyesBlink
	return f
}()

// ── Researcher (violet, mortarboard cap) ──────────────────────────
var spriteResBase = func() spriteFrame {
	f := bodyBase(spV, spV, eyesOpen)
	f[0] = row6(spK, spV, spV, spV, spV, spK) // mortarboard flat top
	f[1] = row6(spBG, spV, spV, spV, spV, spBG)
	f[3] = row6(spK, spV, spE, spV, spE, spK) // violet glasses frames
	return f
}()
var spriteResBlink = func() spriteFrame {
	f := spriteResBase
	f[3] = row6(spK, spV, spK, spV, spK, spK)
	return f
}()

// ── Worker (gray, plain) ───────────────────────────────────────────
var spriteWrkBase = func() spriteFrame {
	return bodyBase(spH, spG, eyesOpen)
}()
var spriteWrkBlink = func() spriteFrame {
	f := spriteWrkBase
	f[3] = eyesBlink
	return f
}()

// ─── Panic / stuck frame (arms up, red tinge) ─────────────────────────────────

func makePanicFrame(base spriteFrame) spriteFrame {
	f := base
	body := f[6][0]                             // grab body colour from row 6
	f[5] = row6(body, spBG, body, body, spBG, body) // arms raised
	f[6] = row6(spBG, body, spBG, spBG, body, spBG)
	return f
}

// ─── Public API ───────────────────────────────────────────────────────────────

const SpritePanelBG = lipgloss.Color("#0a1628") // must match sidebar bg

// GetAgentSprite returns the rendered 6×4-cell sprite for the given role,
// status and animation frame index (0–7).
func GetAgentSprite(role, status string, frame int) string {
	return renderSprite(pickSpriteFrame(role, status, frame), SpritePanelBG)
}

// GetAgentSpriteTall returns a double-height 6×8-cell sprite for the detail pane portrait.
func GetAgentSpriteTall(role, status string, frame int) string {
	return renderSpriteTall(pickSpriteFrame(role, status, frame), SpritePanelBG)
}

// GetAgentSpriteHead returns a single-line 6-char head sprite for sidebar use.
func GetAgentSpriteHead(role, status string, frame int) string {
	return renderSpriteHead(pickSpriteFrame(role, status, frame), SpritePanelBG)
}

func pickSpriteFrame(role, status string, frame int) spriteFrame {
	// blink happens on frame 7 for all roles regardless of status
	blink := frame == 7

	switch status {
	case "stuck":
		base, _ := roleBaseFrames(role)
		if frame%2 == 0 {
			return makePanicFrame(base)
		}
		return base

	case "coding", "testing":
		if role == "senior-dev" {
			if frame%2 == 0 {
				return spriteDevType
			}
			return spriteDevBase
		}
		base, blinkF := roleBaseFrames(role)
		if blink {
			return blinkF
		}
		return base

	default:
		base, blinkF := roleBaseFrames(role)
		if blink {
			return blinkF
		}
		return base
	}
}

func roleBaseFrames(role string) (base, blinkF spriteFrame) {
	switch role {
	case "orchestrator":
		return spriteOrchBase, spriteOrchBlink
	case "senior-dev":
		return spriteDevBase, spriteDevBlink
	case "qa-agent":
		return spriteQABase, spriteQABlink
	case "devops-agent":
		return spriteOpsBase, spriteOpsBlink
	case "researcher":
		return spriteResBase, spriteResBlink
	default:
		return spriteWrkBase, spriteWrkBlink
	}
}
