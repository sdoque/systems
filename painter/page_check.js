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
                 getElementById: function () { return fakeNode('div'); },
                 // The panel escapes text by putting it through a detached node
                 // and reading back the markup, so the fake has to actually
                 // escape — a stub returning '' would let an injection through
                 // this check unnoticed.
                 createElement: function (tag) {
                   var n = fakeNode(tag);
                   Object.defineProperty(n, 'innerHTML', {
                     get: function () {
                       return String(this.textContent)
                         .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
                     },
                     set: function (v) { this._html = v; }, configurable: true });
                   return n;
                 } };
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
  { name: 'thermostat', level: 'identified', doc: 'http://10.0.0.33:20152/thermostat/doc',
    assets: [{ name: 'controller_1', mission: 'control',
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

console.log('a dashed line means the consumer acts, not that the provider is an actuator:');
function dashed() {
  return _created.filter(function (n) { return n.tag === 'path' && n.attrs['stroke-dasharray']; }).length;
}
// Reading an actuator is observation. This is the collector's case: the far end
// is a servo, and the line must not be drawn as though it could move it.
cloud.links[0].mission = 'actuation';
cloud.links[0].action = 'read';
view.scale = 9; _created.length = 0; draw();
check('reading an actuator is not drawn as acting', dashed(), 0);

cloud.links[0].action = 'write';
_created.length = 0; draw();
check('writing to it is', dashed() >= 1, true);

cloud.links[0].action = 'invoke';
_created.length = 0; draw();
check('so is invoking it', dashed() >= 1, true);
cloud.links[0].action = 'read';

console.log('clicking a system says what it is bound to, not merely what it is:');
cloud.hosts[0].systems[0].assets[0].wants[0].satisfied = false;
view.scale = 5; _created.length = 0; draw();
selectedId = 'canbus/thermostat';
renderPanel();
check('the panel opens', panel.hidden, false);
check('it names the system', panel.innerHTML.indexOf('thermostat') >= 0, true);
check('it names the asset', panel.innerHTML.indexOf('controller_1') >= 0, true);
check('it marks the unmet want', panel.innerHTML.indexOf('nothing in this cloud offers it') >= 0, true);
check('it shows what the system is bound to', panel.innerHTML.indexOf('rotation') >= 0, true);
check('it shows the provider it is bound to', panel.innerHTML.indexOf('Servo_1') >= 0, true);

check('it links to the system documentation',
      panel.innerHTML.indexOf('http://10.0.0.33:20152/thermostat/doc') >= 0, true);

console.log('an unmet want says which of its two causes it is:');
check('nobody offers it', panel.innerHTML.indexOf('nothing in this cloud offers it') >= 0, true);
cloud.hosts[0].systems[1].assets[0].provides = [{ definition: 'temperature' }];
_created.length = 0; draw();
check('somebody offers it but is not bound',
      panel.innerHTML.indexOf('offered by parallax/Servo_1, but not bound to it') >= 0, true);
cloud.hosts[0].systems[1].assets[0].provides = [];

console.log('the panel survives the picture being rebuilt, and closes cleanly:');
_created.length = 0; draw();
check('still open after a redraw', panel.hidden, false);
selectedId = null; renderPanel();
check('closing empties it', panel.hidden, true);
selectedId = 'canbus/no-such-system'; renderPanel();
check('a selection that has gone does not linger', panel.hidden, true);

console.log('a name from the graph cannot become markup:');
cloud.hosts[0].systems[0].assets[0].name = '<img src=x onerror=alert(1)>';
_created.length = 0; draw();
selectedId = 'canbus/thermostat'; renderPanel();
check('the tag is escaped', panel.innerHTML.indexOf('<img') < 0, true);
check('and still shown as text', panel.innerHTML.indexOf('&lt;img') >= 0, true);

console.log(failures ? failures + ' FAILURES' : 'all checks passed');
