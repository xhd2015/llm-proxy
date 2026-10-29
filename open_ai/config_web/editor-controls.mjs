// Shared editor behavior. All writes still go through the existing Save button.
export const jsonFoldingOptions = Object.freeze({
  folding: true,
  foldingStrategy: 'auto',
  showFoldingControls: 'always',
  foldingHighlight: true,
});

export function saveShortcutLabel(platform = globalThis.navigator?.platform || '') {
  return /Mac|iPhone|iPad/.test(platform) ? '⌘S' : 'Ctrl+S';
}

// Bind once per editor instance. The action remains active when Save is disabled
// so Cmd/Ctrl+S does not fall through to the browser's Save Page dialog.
export function bindOnDiskSave(editor, button, isCurrentPane) {
  button.title = `Save (${saveShortcutLabel()})`;
  const action = editor.addAction({
    id: 'save-on-disk-file',
    label: 'Save on-disk file',
    // Monaco keybinding encoding: KeyMod.CtrlCmd (1 << 11) | KeyCode.KeyS (49).
    // The vendored API omits these enum exports; keep the encoding local.
    keybindings: [(1 << 11) | 49],
    precondition: 'editorTextFocus && !editorReadonly',
    run: () => {
      if (editor.hasTextFocus() && isCurrentPane() && !button.disabled) button.click();
    },
  });
  const onDispose = editor.onDidDispose(() => action.dispose());
  return {dispose() { onDispose.dispose(); action.dispose(); }};
}

// Monaco intentionally turns folding off in diff sub-editors, even when the
// parent options enable it. Re-enable it through their public editor API after
// each parent option update. Preserve view state because the forced off/on
// transition would otherwise lose collapsed ranges during ordinary re-renders.
const diffOptions = new WeakMap();

export function updateDiffOptions(diff, options, language) {
  const previous = diffOptions.get(diff) || {};
  const changed = Object.entries(options).some(([key, value]) => previous[key] !== value);
  const editors = [diff.getOriginalEditor(), diff.getModifiedEditor()];
  const states = changed && language === 'json' ? editors.map(editor => editor.saveViewState()) : [];
  // Typing re-renders the toolbar too. Do not reset the parent diff (or restore
  // its cursor) on every keystroke when the actual options have not changed.
  if (changed) {
    diff.updateOptions(options);
    diffOptions.set(diff, {...previous, ...options});
  }
  editors.forEach((editor, i) => {
    editor.updateOptions(language === 'json' ? jsonFoldingOptions : {folding: false});
    if (states[i]) editor.restoreViewState(states[i]);
  });
}
