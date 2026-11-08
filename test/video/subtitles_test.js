/* Tests of test/video/subtitles.js: node test/video/subtitles_test.js
 * (make cockpit-test runs it). */
"use strict";
const assert = require("assert");
const { Subtitles, stamp, assStamp } = require("./subtitles");

let t = 0;
const s = new Subtitles(() => t);
// Long enough to read: 2.5 s at least, 70 ms per character.
assert.strictEqual(s.show("Short."), 2500);
assert.strictEqual(s.show("x".repeat(100)), 7000);
assert.strictEqual(s.show("Held.", 9000), 9000);
// Each cue ends where the next starts; a title card ends the one shown.
t = 0;
const s2 = new Subtitles(() => t);
s2.show("One {brace} and a back\\slash");
t = 3200;
s2.show("Two");
t = 3661005;
s2.end();
s2.end(); // nothing shown: no change
assert.deepStrictEqual(s2.cues.map(c => [c.start, c.end]), [[0, 3200], [3200, 3661005]]);
// A cue never ends where it starts.
const s3 = new Subtitles(() => 50);
s3.show("x");
s3.end();
assert.strictEqual(s3.cues[0].end, 51);

assert.strictEqual(stamp(3661005, ","), "01:01:01,005");
assert.strictEqual(stamp(999, "."), "00:00:00.999");
assert.strictEqual(assStamp(3661005), "1:01:01.00");
assert.strictEqual(assStamp(1234), "0:00:01.23");

assert.strictEqual(s2.srt(), "1\n00:00:00,000 --> 00:00:03,200\nOne {brace} and a back\\slash\n\n2\n00:00:03,200 --> 01:01:01,005\nTwo\n");
assert.ok(s2.vtt().startsWith("WEBVTT\n\n00:00:00.000 --> 00:00:03.200\n"));
const ass = s2.ass();
assert.ok(ass.includes("PlayResX: 1920") && ass.includes("PlayResY: 1080"));
// ASS override braces and backslashes are escaped: the text shows as is.
assert.ok(ass.includes("Dialogue: 0,0:00:00.00,0:00:03.20,Default,,0,0,0,,One \\{brace\\} and a back\\\\slash"), ass);
// The loop's strip: one line per stage span, the current stage lit (orange,
// bold), those before it marked (green), the rest plain.
t = 0;
const s4 = new Subtitles(() => t, ["Observe", "Decide", "Act"]);
s4.stage("Observe");
t = 2000;
s4.stage("Decide");
t = 5000;
s4.stage(null);
assert.deepStrictEqual(s4.spans.map(x => [x.start, x.end, x.index]), [[0, 2000, 0], [2000, 5000, 1]]);
assert.throws(() => s4.stage("Dream"), /no stage Dream/);
assert.strictEqual(s4.loopLine(1), "{\\c&H78BA30&}Observe{\\r}  ›  {\\c&H3F7CFE&\\b1}Decide{\\r}  ›  Act");
const ass4 = s4.ass();
assert.ok(ass4.includes("Style: Loop,"), ass4);
assert.ok(ass4.includes("Dialogue: 0,0:00:02.00,0:00:05.00,Loop,,0,0,0,,{\\c&H78BA30&}Observe"), ass4);
// A stage still current when the file is written ends now.
t = 0;
const s5 = new Subtitles(() => t, ["Observe"]);
s5.stage("Observe");
t = 1500;
assert.ok(s5.ass().includes("Dialogue: 0,0:00:00.00,0:00:01.50,Loop"), s5.ass());
console.log("subtitles: ok");
