// Run with browser-agent session run against an editor opened on a temporary fixture.
(async () => {
  const $ = id => document.getElementById(id);
  const assert = (value, message) => { if (!value) throw new Error(message); };
  const wait = async (test) => {
    const end = Date.now() + 5000;
    while (!test()) {
      if (Date.now() > end) throw new Error('UI condition timed out');
      await new Promise(resolve => setTimeout(resolve, 25));
    }
  };
  const clickTab = name => document.querySelector(`[data-tab="${name}"]`).click();
  const edit = (input, value) => { input.value = value; input.dispatchEvent(new Event('input', {bubbles:true})); input.dispatchEvent(new Event('change', {bubbles:true})); };
  const field = (label, index = 0) => [...document.querySelectorAll('label.field')].filter(el => el.querySelector('span').textContent === label)[index].querySelector('input,select,textarea');
  const valid = () => $('diagnostics').textContent === 'Config valid.';
  await wait(valid);
  assert(/\/(?:private\/)?tmp\/llm-proxy-web-/.test($('path').textContent), 'Use a temporary llm-proxy-web-* fixture, never a home config');
  clickTab('raw');
  const original = JSON.parse($('raw').value);
  assert(!location.hash && !location.search, 'Editor URL should not need credentials');
  clickTab('models');
  const branch = document.querySelector('[data-level="model"][data-model-index="0"]');
  assert(branch && branch.parentElement.parentElement.dataset.level === 'provider', 'Missing provider/model nesting');
  assert(branch.querySelector('ul').hidden, 'Base branches should start collapsed');
  if (original.models[0].variants?.length) {
    const variant = original.models[0].variants[0];
    const query = variant.clientModelName;
    edit($('search'),query);
    const leaf = document.querySelector('[data-model-index="0"] [data-variant-index="0"]');
    assert(leaf && !leaf.parentElement.hidden, 'Search did not expose matching variant');
    leaf.querySelector('button').click();
    assert(field('Client name').value === query, 'Variant did not open its own editor');
    edit($('search'),'');
    assert(document.querySelector('[data-model-index="0"] > ul').hidden,'Selecting a search result changed saved expansion');
    assert(![...document.querySelectorAll('label.field span')].some(el=>el.textContent==='Upstream model'), 'Variant edits base-only fields');
    edit(field('Display name'),'Variant-only edit');
    await wait(valid);
    clickTab('raw');
    const expectedVariant = structuredClone(original);
    expectedVariant.models[0].variants[0].displayName = 'Variant-only edit';
    assert(JSON.stringify(JSON.parse($('raw').value))===JSON.stringify(expectedVariant),'Variant edit changed base or sibling fields');
    edit($('raw'),JSON.stringify(original,null,2));await wait(valid);clickTab('models');
    edit($('search'),query);edit($('search'),'');
    assert(document.querySelector('[data-model-index="0"] > ul').hidden,'Search changed saved expansion state');
  }
  const provider = original.providers[0];
  const providerRow = [...document.querySelectorAll('[data-level="provider"] > .tree-row')].find(row=>row.querySelector('.entry').textContent===provider.name);
  providerRow.querySelector('.entry').click();
  assert(field('Provider name').value===provider.name,'Provider row did not open settings');
  document.querySelector(`[aria-label="Add model to ${provider.name}"]`).click();
  assert(field('Provider').value===provider.name,'Add model was not scoped to provider');
  clickTab('raw');edit($('raw'),JSON.stringify(original,null,2));await wait(valid);
  const nextName = original.models[0].displayName === 'Browser regression model' ? 'Browser regression model 2' : 'Browser regression model';
  clickTab('models');
  edit(field('Display name'), nextName);
  await wait(() => !$('save').disabled);
  clickTab('raw');
  const expected = structuredClone(original);
  expected.models[0].displayName = nextName;
  assert(JSON.stringify(JSON.parse($('raw').value)) === JSON.stringify(expected), 'Form edit changed unrelated fields');
  const validText = $('raw').value;
  edit($('raw'), '{');
  await wait(() => $('diagnostics').textContent.includes('Error:'));
  assert($('save').disabled, 'Malformed JSON can be saved');
  edit($('raw'), validText);
  await wait(valid);
  clickTab('models');
  const mode = field('Input mode');
  edit(mode, 'custom');
  const image = [...document.querySelectorAll('.checks label')].find(el => el.textContent === 'image').querySelector('input');
  image.checked = false; image.dispatchEvent(new Event('change', {bubbles:true}));
  await wait(valid);
  clickTab('raw');
  const inputs = JSON.parse($('raw').value).models[0].inputs;
  assert(inputs[0].type === 'text' && inputs[1].disabled === true, 'Modality form lost disabled flag');
  edit($('raw'), validText);
  await wait(valid);
  clickTab('models');
  const addVariant = [...document.querySelectorAll('button')].find(el => el.textContent === '+ Add variant');
  addVariant.click();
  await wait(valid);
  assert(document.querySelector('[data-level="variant"] [aria-pressed="true"]'), 'Added variant was not selected');
  clickTab('raw');
  const variant = JSON.parse($('raw').value).models[0].variants.at(-1);
  assert(!('reasoning' in variant) && !('inputs' in variant), 'New variant does not inherit');
  const addedIndex = JSON.parse($('raw').value).models[0].variants.length - 1;
  clickTab('models');
  const expand = document.querySelector('[data-model-index="0"] .tree-toggle');
  if (expand.getAttribute('aria-expanded') === 'false') expand.click();
  document.querySelector(`[data-model-index="0"] [data-variant-index="${addedIndex}"] button`).click();
  const confirm = window.confirm;
  try {
    window.confirm = () => true;
    [...document.querySelectorAll('button')].find(el=>el.textContent==='Delete variant').click();
  } finally { window.confirm = confirm; }
  assert(field('Upstream model').value===original.models[0].providerModelName,'Deleting variant did not select its base');
  clickTab('raw');
  assert(JSON.stringify(JSON.parse($('raw').value))===JSON.stringify(JSON.parse(validText)),'Variant deletion changed other fields');
  const duplicated = JSON.parse(validText);
  duplicated.models.push(structuredClone(duplicated.models[0]));
  edit($('raw'), JSON.stringify(duplicated));
  await wait(() => $('diagnostics').textContent.includes('duplicate clientModelName'));
  assert($('save').disabled, 'Duplicate route can be saved');
  edit($('raw'), validText);
  await wait(() => !$('save').disabled);
  clickTab('preview');
  await wait(() => $('preview-content').textContent.includes('llm-proxy-providers:'));
  assert($('preview-content').textContent.includes('apiKeyEnv:'), 'Missing credential references');
  const previews = (await (await fetch('/api/validate', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({text:validText})})).json()).previews;
  const originalCopy = navigator.clipboard.writeText;
  const originalClick = HTMLAnchorElement.prototype.click;
  const originalURL = URL.createObjectURL;
  let copied, downloaded, blob;
  URL.createObjectURL = value => { blob = value; return originalURL.call(URL, value); };
  navigator.clipboard.writeText = async value => { copied = value; };
  HTMLAnchorElement.prototype.click = function() { downloaded = {name:this.download, url:this.href}; };
  try {
    for (const runner of ['codex', 'grok', 'dsh']) {
      document.querySelector('[data-runner="' + runner + '"]').click();
      assert($('preview-content').textContent === previews[runner].content, runner + ' preview differs from draft export');
      assert(!$('copy').disabled && !$('download').disabled, runner + ' actions disabled');
      $('copy').click();
      await wait(() => copied === previews[runner].content);
      $('download').click();
      assert(downloaded.name === 'llm-proxy-' + runner + '.' + previews[runner].format, runner + ' download filename');
      assert(await blob.text() === previews[runner].content, runner + ' download content');
    }
  } finally {
    navigator.clipboard.writeText = originalCopy;
    HTMLAnchorElement.prototype.click = originalClick;
    URL.createObjectURL = originalURL;
  }
  $('save').click();
  await wait(() => $('status').textContent === 'Saved');
  assert($('message').textContent.includes('Backup:'), 'Save did not report backup');
  $('reload').click();
  await wait(() => $('message').textContent === '');
  clickTab('raw');
  assert($('raw').value === validText, 'Reload differs from saved draft');
  clickTab('providers');
  assert(field('Provider name').value === original.providers[0].name, 'Provider form did not load');
  clickTab('models');
  await wait(valid);
  return {passed: ['token-free access', 'provider tree', 'variant-only edits', 'search ancestry and expansion', 'provider-scoped add', 'form round-trip', 'malformed repair', 'modalities', 'variant inheritance', 'duplicate refusal', 'DSH preview', 'save backup', 'reload', 'provider form']};
})()
