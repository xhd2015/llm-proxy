// Compare a generated TOML export with the runner file's own [model.*] run.
// The Grok target is compare-only: its model tables are shared with other
// tools, so the editor reports what differs and never writes. Keeping the
// comparison here (a pure module) makes it testable without a browser.

// modelTables parses the [model.<id>] tables of a TOML document into an ordered
// map of id → body lines. It ignores [models] (the global defaults table),
// [model_providers.*], and [[...]] array-of-tables headers, which belong to the
// user rather than to the export.
export function modelTables(text) {
  const tables = new Map();
  let current = null;
  for (const line of String(text || '').split('\n')) {
    if (line.startsWith('[')) {
      const id = modelTableID(line);
      current = null;
      if (id !== '') {
        if (!tables.has(id)) current = [];
        if (current) tables.set(id, current);
      }
      continue;
    }
    if (current && line.trim() !== '') current.push(line.trim());
  }
  return tables;
}

// modelTableID returns the id of a [model.<id>] header, or '' for any other
// table header. Both quoted ([model."a.b"]) and bare ([model.a]) keys match; a
// nested table ([model.a.extra]) belongs to model a, and a bare key stops at its
// first dot while a quoted key keeps its dots.
export function modelTableID(line) {
  if (!line.startsWith('[model.') || line.startsWith('[[')) return '';
  const match = /^\[model\.(?:"([^"]*)"|([^\]\s.]+))(?:\.[^\]\s]*)?\]\s*$/.exec(line);
  if (!match) return '';
  return match[1] !== undefined ? match[1] : match[2];
}

// modelRunSummary compares the export against the file's model run: which ids
// both define, which of those differ, and which side each id only appears on.
export function modelRunSummary(preview, disk) {
  const generated = modelTables(preview);
  const onDisk = modelTables(disk);
  const shared = [];
  const different = [];
  const previewOnly = [];
  const fileOnly = [];
  for (const id of generated.keys()) {
    if (!onDisk.has(id)) {
      previewOnly.push(id);
      continue;
    }
    shared.push(id);
    if (generated.get(id).join('\n') !== onDisk.get(id).join('\n')) different.push(id);
  }
  for (const id of onDisk.keys()) {
    if (!generated.has(id)) fileOnly.push(id);
  }
  return {
    generated: generated.size,
    onDisk: onDisk.size,
    shared,
    different,
    previewOnly,
    fileOnly,
  };
}

// modelRunNotice is the one-line summary shown next to the panes. The differing
// shared keys lead, because they are the part a reader must decide about: the
// same model id can point at a different proxy in each file.
export function modelRunNotice(summary) {
  const {generated, onDisk, different, shared, previewOnly, fileOnly} = summary;
  const parts = [`${generated} generated`, `${onDisk} in file`];
  if (different.length) parts.push(`${different.length} shared key${different.length === 1 ? '' : 's'} differ`);
  if (previewOnly.length) parts.push(`${previewOnly.length} new`);
  if (fileOnly.length) parts.push(`${fileOnly.length} file-only`);
  return parts.join(' · ');
}
