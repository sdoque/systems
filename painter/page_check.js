// A harness for the page's drawing code, run with:
//
//	osascript -l JavaScript page_check.js
//
// macOS ships a JavaScript engine, so the drawing can be executed and asked what
// it produced. It is not a browser: there is no layout, no paint and no SMIL, so
// this proves the code runs and emits the right elements, not that the result
// looks right. That distinction matters — it has caught two faults that Go tests
// could not see and would not have caught a third that was purely visual.
//
// The script is read from page.go rather than copied, so it cannot drift.
ObjC.import('Foundation');

function readPage() {
  var src = $.NSString.stringWithContentsOfFileEncodingError(
    'page.go', $.NSUTF8StringEncoding, null).js;
  return src.substring(src.indexOf('<script>') + 8, src.indexOf('</script>'))
            .replace('refresh();', '').replace('setInterval(refresh, 5000);', '')
            // eval() gives a strict-mode script its own scope, which would hide
            // every variable this harness needs to inspect.
            .replace('"use strict";', '');
}

var _created = [];
function fakeNode(tag) {
  return { tag: tag, attrs: {}, textContent: '', style: {},
    setAttribute: function (k, v) { this.attrs[k] = String(v); },
    appendChild: function (c) { return c; },
    addEventListener: function () {},
    classList: { add: function () {}, remove: function () {} },
    getBoundingClientRect: function () { return { left: 0, top: 0, width: 1200, height: 900 }; },
    clientWidth: 1200, clientHeight: 900, innerHTML: '',
    setPointerCapture: function () {}, releasePointerCapture: function () {} };
}
var document = { createElementNS: function (ns, tag) { var n = fakeNode(tag); _created.push(n); return n; },
                 getElementById: function () { return fakeNode('div'); } };
var window = { addEventListener: function () {} };
function requestAnimationFrame(fn) { fn(); }
function fetch() { return { then: function () { return this; }, catch: function () { return this; } }; }

eval(readPage());

var failures = 0;
function check(what, got, want) {
  var ok = got === want;
  if (!ok) failures++;
  console.log((ok ? '  ok   ' : '  FAIL ') + what + ' — got ' + got + ', want ' + want);
}
function count(pred) { return _created.filter(pred).length; }
function redRings() { return count(function (n) { return n.tag === 'circle' && n.attrs.stroke === 'var(--open)'; }); }
function redFills() { return count(function (n) { return n.tag === 'circle' && n.attrs.fill === 'var(--gone)'; }); }
function lines() { return count(function (n) { return n.tag === 'path'; }); }

cloud = { name: 'AlphaCloud', hosts: [{ name: 'canbus', systems: [
  { name: 'thermostat', level: 'identified', assets: [{ name: 'controller_1', mission: 'control',
      wants: [{ definition: 'temperature', satisfied: false }] }] },
  { name: 'parallax', level: 'identified', assets: [{ name: 'Servo_1', mission: 'actuation' }] }
]}], links: [{ from: 'canbus/thermostat/controller_1', to: 'canbus/parallax/Servo_1',
               definition: 'rotation', mission: 'actuation' }] };

console.log('a service asked for and not provided is visible at every zoom,');
console.log('because a page left showing the whole cloud is where it will be seen:');
[0.5, 1.3, 2.5, 5.0, 9.0].forEach(function (scale) {
  view.scale = scale; _created.length = 0; draw();
  check('scale ' + scale + ' (detail ' + levelOfDetail() + ') shows the alarm', redRings() >= 1, true);
  check('scale ' + scale + ' draws the line', lines() >= 1, true);
});

console.log('a healthy cloud raises no alarm:');
cloud.hosts[0].systems[0].assets[0].wants[0].satisfied = true;
view.scale = 5; _created.length = 0; draw();
check('no alarm ring', redRings(), 0);
check('no alarm dot', redFills(), 0);

console.log('arrivals and departures are drawn, and stop:');
arrivals.set('canbus/newcomer', Date.now());
departures.set('canbus/ds18b20', { at: Date.now(), x: 10, y: 10, name: 'ds18b20' });
_created.length = 0; draw();
check('the departure is on the picture', redFills() >= 1, true);
check('something is still being announced', animating(), true);
arrivals.set('canbus/newcomer', Date.now() - 9000);
departures.set('canbus/ds18b20', { at: Date.now() - 9000, x: 10, y: 10, name: 'ds18b20' });
check('announcements expire', animating(), false);

console.log(failures ? failures + ' FAILURES' : 'all checks passed');
