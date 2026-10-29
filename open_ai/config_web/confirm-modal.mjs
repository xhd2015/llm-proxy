// App-owned confirmations only. Browser navigation/unload warnings stay native.
export function createConfirmModal(dialog, {fallbackFocus = () => null} = {}) {
  const doc = dialog.ownerDocument;
  const title = dialog.querySelector('[data-confirm-title]');
  const message = dialog.querySelector('[data-confirm-message]');
  const details = dialog.querySelector('[data-confirm-details]');
  const items = dialog.querySelector('[data-confirm-items]');
  const cancel = dialog.querySelector('[data-confirm-cancel]');
  const accept = dialog.querySelector('[data-confirm-accept]');
  let pending = null;

  cancel.addEventListener('click', () => dialog.close('cancel'));
  accept.addEventListener('click', () => dialog.close('confirm'));
  dialog.addEventListener('cancel', event => {
    event.preventDefault();
    dialog.close('cancel');
  });
  dialog.addEventListener('close', () => {
    if (!pending) return;
    const {resolve, returnFocus} = pending;
    pending = null;
    resolve(dialog.returnValue === 'confirm');
    // Let the awaiting action clear its pending/disabled state first.
    queueMicrotask(() => {
      if (pending) return; // A second confirmation may already be open.
      const target = returnFocus?.isConnected && !returnFocus.disabled ? returnFocus : fallbackFocus();
      target?.focus({preventScroll: true});
    });
  });
  dialog.addEventListener('keydown', event => {
    // Repeating Save while its confirmation is open must not save the webpage.
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
      event.preventDefault();
      event.stopPropagation();
    }
    if (event.key !== 'Tab') return;
    const targets = [...dialog.querySelectorAll('button, [tabindex="0"]')]
      .filter(element => !element.disabled && !element.closest('[hidden]'));
    const index = targets.indexOf(doc.activeElement);
    if (index < 0 || (!event.shiftKey && index === targets.length - 1) || (event.shiftKey && index === 0)) {
      event.preventDefault();
      (event.shiftKey ? targets.at(-1) : targets[0])?.focus();
    }
  });

  return {
    get pending() { return pending !== null; },
    confirm({title: heading, message: description, details: context = '', items: values = [], confirmLabel = 'Continue', danger = true, returnFocus = doc.activeElement}) {
      // Never stack dialogs or replace a decision already being reviewed.
      if (pending) return Promise.resolve(false);
      title.textContent = heading;
      message.textContent = description;
      details.textContent = context;
      details.hidden = !context;
      items.replaceChildren(...values.map(value => {
        const item = doc.createElement('li');
        item.textContent = value;
        return item;
      }));
      items.parentElement.hidden = !values.length;
      accept.textContent = confirmLabel;
      accept.classList.toggle('danger-primary', danger);
      dialog.returnValue = '';
      return new Promise((resolve, reject) => {
        pending = {resolve, returnFocus};
        try {
          dialog.showModal();
          cancel.focus({preventScroll: true});
        } catch (error) {
          pending = null;
          reject(error);
        }
      });
    },
  };
}
