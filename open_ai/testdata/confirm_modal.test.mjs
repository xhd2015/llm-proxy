import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {runInNewContext} from 'node:vm';
import {createConfirmModal} from '../config_web/confirm-modal.mjs';

function fixture() {
  const doc = {activeElement: null};
  class Element extends EventTarget {
    constructor(name) {super(); this.name = name; this.isConnected = true; this.disabled = false; this.hidden = false; this.textContent = ''; this.classes = new Set(); this.classList = {toggle: (key, value) => value ? this.classes.add(key) : this.classes.delete(key)};}
    focus() {doc.activeElement = this;}
    closest() {return this.hidden ? this : null;}
    replaceChildren(...items) {this.children = items;}
  }
  doc.createElement = name => new Element(name);
  const fields = Object.fromEntries(['title','message','details','items','cancel','accept'].map(name => [name, new Element(name)]));
  fields.items.parentElement = new Element('list');
  const dialog = new Element('dialog');
  dialog.ownerDocument = doc;
  dialog.querySelector = selector => fields[selector.match(/data-confirm-(\w+)/)[1]];
  dialog.querySelectorAll = () => [fields.items.parentElement, fields.cancel, fields.accept];
  dialog.showModal = () => {dialog.open = true;};
  dialog.close = value => {dialog.returnValue = value; dialog.open = false; dialog.dispatchEvent(new Event('close'));};
  const origin = new Element('origin'); origin.focus();
  const fallback = new Element('fallback');
  const modal = createConfirmModal(dialog, {fallbackFocus: () => fallback});
  const open = options => modal.confirm({title: 'Delete?', message: 'Review this action.', confirmLabel: 'Delete', ...options});
  return {doc, dialog, fields, origin, fallback, modal, open};
}

function key(dialog, name, options = {}) {
  const event = new Event('keydown', {cancelable: true});
  Object.assign(event, {key: name, shiftKey: false, metaKey: false, ctrlKey: false, ...options});
  dialog.dispatchEvent(event);
  return event;
}

test('confirmation waits for the explicit button and defaults focus to Cancel', async () => {
  const f = fixture();
  const result = f.open();
  let settled = false; result.then(() => {settled = true;});
  await Promise.resolve();
  assert.equal(settled, false);
  assert.equal(f.modal.pending, true);
  assert.equal(f.doc.activeElement, f.fields.cancel);
  f.fields.accept.dispatchEvent(new Event('click'));
  assert.equal(await result, true);
  assert.equal(f.modal.pending, false);
});

test('Cancel and Escape resolve false and restore focus', async () => {
  const f = fixture();
  const first = f.open();
  f.fields.cancel.dispatchEvent(new Event('click'));
  assert.equal(await first, false);
  assert.equal(f.doc.activeElement, f.origin);
  const second = f.open();
  const escape = new Event('cancel', {cancelable: true});
  f.dialog.dispatchEvent(escape);
  assert.equal(escape.defaultPrevented, true);
  assert.equal(await second, false);
  assert.equal(f.dialog.open, false);
});

test('pending decision cannot be replaced or stacked by repeat activation', async () => {
  const f = fixture();
  const first = f.open({title: 'First decision'});
  assert.equal(await f.open({title: 'Second decision'}), false);
  assert.equal(f.fields.title.textContent, 'First decision');
  f.fields.cancel.dispatchEvent(new Event('click'));
  assert.equal(await first, false);
});

test('untrusted labels render as text and long lists stay separate from the message', async () => {
  const f = fixture(), unsafe = '<img src=x onerror=alert(1)>';
  const result = f.open({title: unsafe, details: unsafe, items: [unsafe, ...Array.from({length: 80}, (_, i) => `route-${i}`)]});
  assert.equal(f.fields.title.textContent, unsafe);
  assert.equal(f.fields.details.textContent, unsafe);
  assert.equal(f.fields.items.children[0].textContent, unsafe);
  assert.equal(f.fields.items.children.length, 81);
  assert.equal(f.fields.items.parentElement.hidden, false);
  f.fields.cancel.dispatchEvent(new Event('click'));
  await result;
  const next = f.open({danger: false});
  assert.equal(f.fields.items.children.length, 0);
  assert.equal(f.fields.details.hidden, true);
  assert.equal(f.fields.items.parentElement.hidden, true);
  assert.equal(f.fields.accept.classes.has('danger-primary'), false);
  f.fields.cancel.dispatchEvent(new Event('click'));
  await next;
});

test('Tab wraps inside the modal and Cmd/Ctrl+S cannot open browser Save Page', async () => {
  const f = fixture(), result = f.open();
  f.fields.accept.focus();
  assert.equal(key(f.dialog, 'Tab').defaultPrevented, true);
  assert.equal(f.doc.activeElement, f.fields.cancel);
  key(f.dialog, 'Tab', {shiftKey: true});
  assert.equal(f.doc.activeElement, f.fields.accept);
  for (const modifier of ['metaKey','ctrlKey']) assert.equal(key(f.dialog, 's', {[modifier]: true}).defaultPrevented, true);
  f.fields.cancel.dispatchEvent(new Event('click'));
  await result;
});

test('focus falls back safely if the originating control was removed', async () => {
  const f = fixture(), result = f.open();
  f.origin.isConnected = false;
  f.fields.accept.dispatchEvent(new Event('click'));
  await result;
  assert.equal(f.doc.activeElement, f.fallback);
});

test('a new decision retains focus instead of restoring the previous dialog origin', async () => {
  const f = fixture();
  const first = f.open();
  let second;
  first.then(() => {second = f.open({title: 'Next'});});
  f.fields.accept.dispatchEvent(new Event('click'));
  await first;
  assert.equal(f.fields.title.textContent, 'Next');
  assert.equal(f.doc.activeElement, f.fields.cancel);
  f.fields.cancel.dispatchEvent(new Event('click'));
  await second;
});

test('actual pane guards preserve Cancel and restore loaded content only on confirmation', async () => {
  const source = readFileSync(new URL('../config_web/app.js', import.meta.url), 'utf8');
  for (const name of ['mayDiscardSync', 'mayDiscardCompare']) {
    const code = source.match(new RegExp(`async function ${name}\\(\\) \\{[\\s\\S]*?^\\}`, 'm'))[0];
    for (const accepted of [false, true]) {
      const area = {value: 'edited'};
      let model = 'edited';
      const ctx = {syncDirty: true, compareDirty: true, syncData: {disk: 'loaded'}, compareData: {draft: 'loaded'},
        diffModified: {setValue(value) {model = value;}}, compareRight: {setValue(value) {model = value;}},
        $: () => area, confirmAction: async () => accepted};
      const pending = runInNewContext(`${code}; ${name}()`, ctx);
      assert.equal(model, 'edited', 'must await the decision');
      assert.equal(await pending, accepted);
      assert.equal(model, accepted ? 'loaded' : 'edited');
      assert.equal(area.value, accepted ? 'loaded' : 'edited');
      assert.equal(name === 'mayDiscardSync' ? ctx.syncDirty : ctx.compareDirty, !accepted);
    }
  }
});

test('actual field guard clears incomplete input state only after confirmation', async () => {
  const source = readFileSync(new URL('../config_web/app.js', import.meta.url), 'utf8');
  const code = source.match(/async function mayDiscardFields\(\) \{[\s\S]*?^\}/m)[0];
  for (const accepted of [false, true]) {
    const fieldErrors = new Map([['field', 'invalid']]);
    const pending = runInNewContext(`${code}; mayDiscardFields()`, {fieldErrors, confirmAction: async () => accepted});
    assert.equal(fieldErrors.size, 1);
    assert.equal(await pending, accepted);
    assert.equal(fieldErrors.size, accepted ? 0 : 1);
  }
});

test('all app discard guards await the modal; no app-owned native prompts remain', () => {
  const source = readFileSync(new URL('../config_web/app.js', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /\b(?:confirm|alert|prompt)\s*\(/);
  for (const match of source.matchAll(/\b(mayDiscardFields|mayDiscardSync|mayDiscardCompare)\(\)/g)) {
    const prefix = source.slice(Math.max(0, match.index - 24), match.index);
    assert.match(prefix, /(?:async function |await )$/, `unawaited ${match[0]}`);
  }
  assert.match(source, /compareSaving = true;[\s\S]*?await confirmAction\(/);
  assert.match(source, /syncDirty\s*\?\s*syncPaneValue\(\)/, 'background renders must preserve the edited runner pane');
  assert.match(source, /confirmations\.pending && syncDirty/, 'background refresh must not discard a pending confirmation');
  const html = readFileSync(new URL('../config_web/index.html', import.meta.url), 'utf8');
  assert.match(html, /<dialog[^>]+aria-modal="true"[^>]+aria-labelledby="confirm-title"/);
  assert.ok(html.indexOf('</main>') < html.indexOf('<dialog'), 'dialog must not inherit main.inert during Save');
});
