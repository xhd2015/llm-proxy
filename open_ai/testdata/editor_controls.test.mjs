import {test} from 'node:test';
import assert from 'node:assert/strict';
import {bindOnDiskSave, jsonFoldingOptions, saveShortcutLabel, updateDiffOptions} from '../config_web/editor-controls.mjs';

function fixture() {
  let action, onDispose, clicks = 0, disposed = 0, focused = true, current = true;
  const editor = {
    addAction(value) { action = value; return {dispose() { disposed++; }}; },
    hasTextFocus: () => focused,
    onDidDispose(callback) { onDispose = callback; return {dispose() { onDispose = undefined; }}; },
  };
  const button = {disabled: false, click() { clicks++; }};
  bindOnDiskSave(editor, button, () => current);
  return {editor, button, get action() {return action;}, get clicks() {return clicks;}, get disposed() {return disposed;},
    focus(value) {focused = value;}, current(value) {current = value;}, dispose() {onDispose();}};
}

test('save shortcut is a focused editor action and uses the existing button', () => {
  const f = fixture();
  assert.equal(f.action.keybindings[0], (1 << 11) | 49);
  assert.equal(f.action.precondition, 'editorTextFocus && !editorReadonly');
  f.action.run();
  assert.equal(f.clicks, 1);
  f.focus(false); f.action.run();
  f.focus(true); f.current(false); f.action.run();
  assert.equal(f.clicks, 1, 'never save from another pane or without text focus');
});

test('disabled Save still consumes the action without submitting', () => {
  const f = fixture();
  for (const state of ['unchanged', 'saving', 'loading', 'conflict']) {
    f.button.disabled = true;
    f.action.run();
    assert.equal(f.clicks, 0, state);
  }
  assert.equal(f.action.precondition.includes('dirty'), false, 'disabled state must not unbind Cmd/Ctrl+S');
});

test('button state prevents repeated submits and disposal removes the binding', () => {
  const f = fixture();
  let submits = 0;
  f.button.click = () => {submits++; f.button.disabled = true;};
  f.action.run(); f.action.run();
  assert.equal(submits, 1);
  assert.equal(f.button.disabled, true);
  f.dispose();
  assert.equal(f.disposed, 1);
});

test('shortcut tooltip follows the platform', () => {
  assert.equal(saveShortcutLabel('MacIntel'), '⌘S');
  assert.equal(saveShortcutLabel('Win32'), 'Ctrl+S');
  assert.equal(saveShortcutLabel('Linux x86_64'), 'Ctrl+S');
});

test('JSON folding uses native controls and survives parent diff updates', () => {
  const events = [];
  const editors = ['original', 'modified'].map(name => ({
    state: {folds: [2, 5], cursor: 8},
    saveViewState() {return {...this.state};},
    updateOptions(options) {events.push([name, options]);},
    restoreViewState(state) {this.state = state;},
  }));
  const diff = {
    getOriginalEditor: () => editors[0], getModifiedEditor: () => editors[1],
    updateOptions() {events.push(['parent']); editors.forEach(editor => {editor.state = {};});},
  };
  updateDiffOptions(diff, {renderSideBySide: true}, 'json');
  assert.equal(events[0][0], 'parent');
  for (const editor of editors) assert.deepEqual(editor.state, {folds: [2, 5], cursor: 8});
  assert.equal(events[1][1], jsonFoldingOptions);
  assert.equal(events[2][1], jsonFoldingOptions);
  assert.equal(jsonFoldingOptions.folding, true);
  assert.equal(jsonFoldingOptions.showFoldingControls, 'always');
  assert.equal(jsonFoldingOptions.foldingStrategy, 'auto');
  updateDiffOptions(diff, {readOnly: true}, 'json');
  for (const editor of editors) assert.deepEqual(editor.state.folds, [2, 5]);
});

test('unchanged toolbar options do not reset folds or restore a typing cursor', () => {
  let parentUpdates = 0, restores = 0;
  const editor = {saveViewState: () => ({}), updateOptions() {}, restoreViewState() {restores++;}};
  const diff = {getOriginalEditor: () => editor, getModifiedEditor: () => editor, updateOptions() {parentUpdates++;}};
  updateDiffOptions(diff, {renderSideBySide: true}, 'json');
  updateDiffOptions(diff, {renderSideBySide: true}, 'json');
  updateDiffOptions(diff, {renderSideBySide: true}, 'json');
  assert.equal(parentUpdates, 1);
  assert.equal(restores, 2);
});

test('non-JSON previews do not restore JSON folding state', () => {
  const updates = [];
  const editor = {updateOptions: options => updates.push(options), saveViewState() {throw new Error('unexpected');}};
  updateDiffOptions({getOriginalEditor: () => editor, getModifiedEditor: () => editor, updateOptions() {}}, {}, 'yaml');
  assert.deepEqual(updates, [{folding: false}, {folding: false}]);
});
