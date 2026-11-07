/* Subtitles of the demonstration videos (test/video/tour.js): cues timed
 * against the recording's start, written as SRT and WebVTT (for players)
 * and as ASS for render.sh to burn into the band below the pages. */
"use strict";

class Subtitles {
    /* now: milliseconds since the recording started. */
    constructor(now) {
        this.now = now;
        this.cues = [];
    }

    /* show starts a cue (ending the one before) and returns how long to
     * hold it so it can be read: at least 2.5 s, 70 ms per character, or
     * hold if longer. */
    show(text, hold = 0) {
        const t = this.now();
        this.end(t);
        this.cues.push({ start: t, end: null, text });
        return Math.max(hold, 2500, text.length * 70);
    }

    /* end ends the cue being shown (a title card, the end). */
    end(t = this.now()) {
        const last = this.cues[this.cues.length - 1];
        if (last && last.end === null) last.end = Math.max(t, last.start + 1);
    }

    srt() {
        return this.cues.map((c, i) => `${i + 1}\n${stamp(c.start, ",")} --> ${stamp(c.end, ",")}\n${c.text}\n`).join("\n");
    }

    vtt() {
        return "WEBVTT\n\n" + this.cues.map(c => `${stamp(c.start, ".")} --> ${stamp(c.end, ".")}\n${c.text}\n`).join("\n");
    }

    /* ass: for 1920×1080, the cues centred in the 120-pixel band at the
     * bottom (render.sh scales the pages to 1920×960 above it). */
    ass() {
        const esc = t => t.replace(/\\/g, "\\\\").replace(/[{}]/g, m => "\\" + m).replace(/\n/g, "\\N");
        return [
            "[Script Info]", "ScriptType: v4.00+", "PlayResX: 1920", "PlayResY: 1080", "WrapStyle: 0", "ScaledBorderAndShadow: yes", "",
            "[V4+ Styles]",
            "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, " +
                "StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding",
            "Style: Default,DejaVu Sans,34,&H00FFFFFF,&H00FFFFFF,&H002C320C,&H002C320C,0,0,0,0,100,100,0,0,1,0,0,2,80,80,22,1", "",
            "[Events]", "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
            ...this.cues.map(c => `Dialogue: 0,${assStamp(c.start)},${assStamp(c.end)},Default,,0,0,0,,${esc(c.text)}`),
        ].join("\n") + "\n";
    }
}

const pad = (n, w) => String(Math.floor(n)).padStart(w, "0");
/* stamp: HH:MM:SS,mmm (SRT) or HH:MM:SS.mmm (WebVTT). */
const stamp = (ms, sep) => `${pad(ms / 3600000, 2)}:${pad(ms / 60000 % 60, 2)}:${pad(ms / 1000 % 60, 2)}${sep}${pad(ms % 1000, 3)}`;
/* assStamp: H:MM:SS.cc. */
const assStamp = ms => `${Math.floor(ms / 3600000)}:${pad(ms / 60000 % 60, 2)}:${pad(ms / 1000 % 60, 2)}.${pad(ms % 1000 / 10, 2)}`;

module.exports = { Subtitles, stamp, assStamp };
