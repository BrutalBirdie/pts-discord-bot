// Progressive enhancements for every page. Without JavaScript the sheets
// still open (popover attribute) and every form posts normally.
'use strict';

(function () {
    document.documentElement.classList.add('js');
    const toastEl = document.getElementById('toast');
    let toastTimer;

    function toast(text) {
        toastEl.textContent = text;
        toastEl.hidden = false;
        clearTimeout(toastTimer);
        toastTimer = setTimeout(() => { toastEl.hidden = true; }, 2600);
    }

    function init(root) {
        if (navigator.clipboard) {
            root.querySelectorAll('button.copy[data-copy]').forEach(function (button) {
                button.hidden = false;
                button.addEventListener('click', function () {
                    navigator.clipboard.writeText(button.dataset.copy).then(function () {
                        button.classList.add('done');
                        button.querySelector('use').setAttribute('href', '#i-check');
                        toast('Copied');
                        setTimeout(function () {
                            button.classList.remove('done');
                            button.querySelector('use').setAttribute('href', '#i-copy');
                        }, 1500);
                    });
                });
            });
        }
        // destructive buttons stay disabled until the name is typed
        root.querySelectorAll('input[data-confirm]').forEach(function (input) {
            const button = input.form.querySelector('button.needs-confirm');
            if (!button) return;
            const update = () => { button.disabled = input.value !== input.dataset.confirm; };
            input.addEventListener('input', update);
            update();
        });
        root.querySelectorAll('button[data-confirm-click]').forEach(function (button) {
            button.addEventListener('click', function (event) {
                if (!confirm(button.dataset.confirmClick)) event.preventDefault();
            });
        });
    }

    window.botpanel = { init, toast };
    init(document);

    // changing the server of a task loads its channels and members
    document.querySelectorAll('select[data-reload]').forEach(function (select) {
        select.addEventListener('change', function () {
            const button = select.form.querySelector('button[name=op][value=reload]');
            if (button) select.form.requestSubmit(button);
        });
    });

    // member pickers: suggestions from /members while typing
    document.querySelectorAll('input[data-members]').forEach(function (input) {
        const list = document.createElement('ul');
        list.className = 'suggestions';
        list.hidden = true;
        list.setAttribute('role', 'listbox');
        input.after(list);
        input.setAttribute('role', 'combobox');
        input.setAttribute('aria-expanded', 'false');
        let hits = [], active = -1, timer, seq = 0;

        function close() {
            list.hidden = true;
            input.setAttribute('aria-expanded', 'false');
            active = -1;
        }
        function show() {
            list.replaceChildren(...hits.map(function (h, i) {
                const li = document.createElement('li');
                li.setAttribute('role', 'option');
                li.textContent = h.name;
                const small = document.createElement('small');
                small.textContent = '@' + h.username;
                li.append(small);
                li.classList.toggle('active', i === active);
                li.addEventListener('mousedown', function (event) {
                    event.preventDefault();
                    pick(h);
                });
                return li;
            }));
            list.hidden = hits.length === 0;
            input.setAttribute('aria-expanded', String(!list.hidden));
        }
        function pick(h) {
            close();
            if (input.dataset.insert) {
                const area = input.form.elements[input.dataset.insert];
                const mention = '<@' + h.id + '>';
                const at = area.selectionStart ?? area.value.length;
                area.value = area.value.slice(0, at) + mention + area.value.slice(area.selectionEnd ?? at);
                area.dispatchEvent(new Event('input', { bubbles: true }));
                input.value = '';
                window.botpanel.toast('Mention of ' + h.name + ' inserted');
            } else {
                input.value = h.label;
            }
        }
        input.addEventListener('input', function () {
            clearTimeout(timer);
            const q = input.value.trim();
            if (q === '') { close(); return; }
            timer = setTimeout(function () {
                const mine = ++seq;
                const params = new URLSearchParams({ q, guild: input.dataset.members });
                fetch('/members?' + params).then((r) => r.ok ? r.json() : []).then(function (data) {
                    if (mine !== seq) return;
                    hits = data;
                    active = hits.length ? 0 : -1;
                    show();
                }).catch(() => {});
            }, 150);
        });
        input.addEventListener('keydown', function (event) {
            if (list.hidden) return;
            if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
                event.preventDefault();
                active = (active + (event.key === 'ArrowDown' ? 1 : hits.length - 1)) % hits.length;
                show();
            } else if (event.key === 'Enter' && active >= 0) {
                event.preventDefault();
                pick(hits[active]);
            } else if (event.key === 'Escape') {
                close();
            }
        });
        input.addEventListener('blur', close);
    });

    // live preview of the message editor
    const previewForm = document.querySelector('form[data-preview-form]');
    if (previewForm) {
        previewForm.addEventListener('input', function (event) {
            const key = event.target.dataset.preview;
            if (!key) return;
            const target = document.querySelector('[data-preview-target="' + key + '"]');
            if (!target) return;
            if (key.startsWith('color-')) {
                target.setAttribute('fill', event.target.value);
                return;
            }
            const link = target.querySelector('a');
            (link || target).textContent = event.target.value;
            if (key === 'content') {
                const counter = document.querySelector('[data-count=content]');
                const n = [...event.target.value].length;
                counter.textContent = n + ' / ' + counter.dataset.max;
                counter.classList.toggle('over', n > Number(counter.dataset.max));
            }
        });
    }

    // a link like /servers/1#sheet-play-2 opens that sheet
    const target = location.hash.startsWith('#sheet-') && document.getElementById(location.hash.slice(1));
    if (target && target.showPopover) target.showPopover();

    // pages with data-live regions refresh them while they are visible
    const live = document.querySelectorAll('[data-live]');
    if (live.length) {
        setInterval(function () {
            if (document.hidden || document.querySelector(':popover-open')) return;
            fetch(location.href, { headers: { Accept: 'text/html' } }).then((r) => r.ok ? r.text() : null).then(function (html) {
                if (!html) return;
                const doc = new DOMParser().parseFromString(html, 'text/html');
                document.querySelectorAll('[data-live]').forEach(function (el) {
                    const fresh = doc.querySelector('[data-live="' + el.dataset.live + '"]');
                    if (fresh && fresh.innerHTML !== el.innerHTML) {
                        el.innerHTML = fresh.innerHTML;
                        init(el);
                    }
                });
            }).catch(() => {});
        }, 5000);
    }

    const account = document.querySelector('form[data-autosave]');
    if (account) {
        account.addEventListener('change', function (event) {
            if (event.target.name === 'show_labels') document.documentElement.classList.toggle('labels', event.target.checked);
            fetch(account.action, {
                method: 'POST',
                body: new URLSearchParams(new FormData(account)),
                headers: { Accept: 'application/json' },
            }).then(
                (r) => toast(r.ok ? 'Saved' : 'Not saved, reload the page and try again'),
                () => toast('Not saved, check your connection'),
            );
        });
    }
})();
