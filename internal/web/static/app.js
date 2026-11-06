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

    function svgIcon(name) {
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'i');
        svg.setAttribute('aria-hidden', 'true');
        const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
        use.setAttribute('href', '#i-' + name);
        svg.append(use);
        return svg;
    }
    function clock(ms) {
        const s = Math.floor(ms / 1000), pad = (n) => String(n).padStart(2, '0');
        return s >= 3600 ? Math.floor(s / 3600) + ':' + pad(Math.floor(s / 60) % 60) + ':' + pad(s % 60) : Math.floor(s / 60) + ':' + pad(s % 60);
    }
    // members have a username, sounds a length
    function subText(h) {
        if (h.username !== undefined) return '@' + h.username;
        return h.duration_ms ? clock(h.duration_ms) : 'length unknown';
    }

    // play steps: the sum of the sounds, or the longest when one is picked at random
    function updateTotal(tokens) {
        const step = tokens.closest('.step');
        const total = step && step.querySelector('[data-total]');
        if (!total) return;
        const random = step.querySelector('input[type=radio][value=random]:checked');
        let ms = 0, unknown = false;
        tokens.querySelectorAll('.tok').forEach(function (tok) {
            const n = Number(tok.dataset.ms) || 0;
            if (!n) unknown = true;
            ms = random ? Math.max(ms, n) : ms + n;
        });
        const cap = Number(total.dataset.cap);
        total.textContent = 'Total ' + clock(ms) + ' / ' + clock(cap) + (unknown ? ', some lengths unknown' : '');
        total.classList.toggle('over', ms > cap);
    }

    // buttons with data-confirm-desc ask once: the first click arms them, the second one goes through
    let armed = null;
    function disarm(focus) {
        if (!armed) return;
        const a = armed;
        armed = null;
        clearTimeout(a.timer);
        a.btn.classList.remove('armed');
        if (a.created) a.lbl.remove(); else a.lbl.textContent = a.text;
        a.btn.removeAttribute('aria-describedby');
        a.line.remove();
        if (focus) a.btn.focus();
    }
    function arm(btn) {
        let lbl = btn.querySelector('.lbl'), created = false;
        if (!lbl) {
            lbl = document.createElement('span');
            lbl.className = 'lbl';
            btn.append(lbl);
            created = true;
        }
        const text = lbl.textContent;
        lbl.textContent = btn.dataset.armedLabel || (text ? text + '?' : 'Confirm?');
        btn.classList.add('armed');
        const line = document.createElement('div');
        line.className = 'confirm-line';
        line.id = 'confirm-line';
        line.setAttribute('role', 'status');
        const msg = document.createElement('span');
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'ib quiet with-label cancel';
        const cancelLbl = document.createElement('span');
        cancelLbl.className = 'lbl';
        cancelLbl.textContent = 'Cancel';
        cancel.append(svgIcon('x'), cancelLbl);
        line.append(msg, cancel);
        (btn.closest('.btns') || btn.closest('li') || btn).after(line);
        btn.setAttribute('aria-describedby', line.id);
        // filled after insertion, screen readers announce changes of a status region, not new ones
        setTimeout(function () { msg.textContent = btn.dataset.confirmDesc + ' Click again to confirm.'; }, 50);
        const timer = btn.hasAttribute('data-irreversible') ? 0 : setTimeout(() => disarm(false), 4000);
        armed = { btn, lbl, text, created, line, timer };
    }
    // capture phase, so an unconfirmed click never reaches the handlers of the button itself
    document.addEventListener('click', function (event) {
        const btn = event.target.closest('button[data-confirm-desc]');
        if (armed && armed.btn !== btn) disarm(Boolean(event.target.closest('.confirm-line .cancel')));
        if (!btn || btn.disabled) return;
        if (armed) {
            disarm(false);
            return;
        }
        if (document.body.dataset.confirm === 'off' && !btn.hasAttribute('data-irreversible')) return;
        event.preventDefault();
        event.stopImmediatePropagation();
        arm(btn);
    }, true);
    document.addEventListener('keydown', function (event) {
        if (event.key === 'Escape' && armed) disarm(true);
    });

    // sound previews, one at a time: a [data-preview-media] button toggles playback and a volume
    // slider replaces the [data-preview-detail] of its row while playing
    let preview = null, volTimer;
    function setPreviewButton(btn, playing) {
        const text = (playing ? 'Stop ' : 'Preview ') + btn.dataset.name;
        btn.setAttribute('aria-pressed', String(playing));
        btn.setAttribute('aria-label', text);
        if (btn.dataset.tip !== undefined) btn.dataset.tip = text;
        btn.querySelector('use').setAttribute('href', playing ? '#i-square' : '#i-music');
    }
    function stopPreview() {
        if (!preview) return;
        const p = preview;
        preview = null;
        p.audio.pause();
        p.audio.removeAttribute('src');
        p.vol.remove();
        if (p.detail) p.detail.hidden = false;
        setPreviewButton(p.btn, false);
    }
    function stopPreviewIn(el) {
        if (preview && el.contains(preview.btn)) stopPreview();
    }
    function saveVolume(id, volume) {
        const csrf = document.querySelector('input[name=csrf]');
        if (!csrf) return;
        clearTimeout(volTimer);
        volTimer = setTimeout(function () {
            fetch('/prefs/preview-volume', {
                method: 'POST',
                body: new URLSearchParams({ csrf: csrf.value, media_id: id, volume }),
                headers: { Accept: 'application/json' },
            }).then(
                (r) => { if (!r.ok) toast('Volume not saved, reload the page and try again'); },
                () => toast('Volume not saved, check your connection'),
            );
        }, 500);
    }
    function startPreview(btn) {
        const id = btn.dataset.previewMedia;
        const row = btn.closest('[data-preview-row], .tok, li');
        const detail = row && row.querySelector('[data-preview-detail]');
        const audio = new Audio('/media/' + id + '/file');
        const vol = document.createElement('span');
        vol.className = 'pv-vol';
        const label = document.createElement('label');
        const text = document.createElement('span');
        text.className = 'vh';
        text.textContent = 'Preview volume';
        const slider = document.createElement('input');
        slider.type = 'range';
        slider.min = '0';
        slider.max = '100';
        slider.value = btn.dataset.vol || '100';
        const out = document.createElement('output');
        out.textContent = slider.value + '%';
        label.append(text, slider);
        vol.append(label, out);
        audio.volume = Number(slider.value) / 100;
        slider.addEventListener('input', function () {
            audio.volume = Number(slider.value) / 100;
            out.textContent = slider.value + '%';
            document.querySelectorAll('[data-preview-media="' + id + '"]').forEach((b) => { b.dataset.vol = slider.value; });
            saveVolume(id, slider.value);
        });
        if (detail) {
            detail.hidden = true;
            detail.after(vol);
        } else {
            btn.after(vol);
        }
        setPreviewButton(btn, true);
        preview = { audio, btn, vol, detail };
        audio.addEventListener('ended', function () { if (preview && preview.audio === audio) stopPreview(); });
        audio.play().catch(function () {
            if (preview && preview.audio === audio) stopPreview();
            toast('The preview could not be played');
        });
    }
    document.addEventListener('click', function (event) {
        const btn = event.target.closest('button[data-preview-media]');
        if (!btn) return;
        event.preventDefault();
        const same = preview && preview.btn === btn;
        stopPreview();
        if (!same) startPreview(btn);
    });

    // pickers: suggestions while typing, from /members or the data-source of a token field
    document.querySelectorAll('input[data-members], [data-source] input[type=search]').forEach(function (input) {
        const list = document.createElement('ul');
        list.className = 'suggestions';
        list.hidden = true;
        list.setAttribute('role', 'listbox');
        // token fields scroll, so the list goes outside the scrolling box
        const tokens = input.closest('[data-tokens]');
        if (tokens) tokens.append(list); else input.after(list);
        const source = (tokens && tokens.dataset.source) || '/members';
        input.setAttribute('role', 'combobox');
        input.setAttribute('aria-expanded', 'false');
        let hits = [], active = -1, timer, seq = 0;

        function picked() {
            return Array.from(tokens.querySelectorAll('.tok input[type=hidden]'), (el) => el.value);
        }
        function tokensChanged() {
            const n = tokens.querySelectorAll('.tok').length, max = Number(tokens.dataset.max);
            input.hidden = n >= max;
            const count = tokens.parentElement.querySelector('[data-count]');
            if (count) count.textContent = n ? n + ' of ' + max : tokens.dataset.empty;
            updateTotal(tokens);
        }
        function removeToken(tok) {
            stopPreviewIn(tok);
            tok.remove();
            tokensChanged();
        }
        function addToken(h) {
            const tok = document.createElement('span');
            tok.className = 'tok';
            tok.title = h.label || h.name;
            if (h.username !== undefined) {
                const av = document.createElement('span');
                av.className = 'av';
                av.setAttribute('aria-hidden', 'true');
                av.textContent = (h.name.trim()[0] || '?').toUpperCase();
                tok.append(av);
            } else {
                tok.dataset.ms = h.duration_ms || 0;
                const pv = document.createElement('button');
                pv.type = 'button';
                pv.className = 'pv';
                pv.dataset.previewMedia = h.id;
                pv.dataset.vol = h.volume ?? 100;
                pv.dataset.name = h.name;
                pv.setAttribute('aria-label', 'Preview ' + h.name);
                pv.setAttribute('aria-pressed', 'false');
                pv.append(svgIcon('music'));
                tok.append(pv);
            }
            const nm = document.createElement('span');
            nm.className = 'nm';
            if (h.username === undefined) nm.dataset.previewDetail = '';
            const un = document.createElement('span');
            un.className = 'un';
            un.textContent = subText(h);
            nm.append(h.name + ' ', un);
            const hidden = document.createElement('input');
            hidden.type = 'hidden';
            hidden.name = tokens.dataset.tokens;
            hidden.value = h.id;
            const btn = document.createElement('button');
            btn.type = 'button';
            btn.className = 'rm';
            btn.setAttribute('aria-label', 'Remove ' + h.name);
            btn.append(svgIcon('x'));
            tok.append(nm, hidden, btn);
            input.before(tok);
            input.value = '';
            tokensChanged();
            tokens.querySelector('.box').scrollTop = 1e6;
            input.focus();
        }
        if (tokens) {
            tokens.addEventListener('click', function (event) {
                const btn = event.target.closest('.tok button.rm');
                if (btn) {
                    event.preventDefault();
                    removeToken(btn.closest('.tok'));
                    input.focus();
                } else if (event.target.classList.contains('box')) {
                    input.focus();
                }
            });
            const step = tokens.closest('.step');
            if (step) step.addEventListener('change', function (event) {
                if (event.target.type === 'radio') updateTotal(tokens);
            });
        }

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
                small.textContent = subText(h);
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
            if (tokens) {
                addToken(h);
            } else if (input.dataset.insert) {
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
                const params = new URLSearchParams({ q });
                if (input.dataset.members !== undefined) params.set('guild', input.dataset.members);
                fetch(source + '?' + params).then((r) => r.ok ? r.json() : []).then(function (data) {
                    if (mine !== seq) return;
                    hits = tokens && tokens.dataset.dupes === undefined ? data.filter((h) => !picked().includes(String(h.id))) : data;
                    active = hits.length ? 0 : -1;
                    show();
                }).catch(() => {});
            }, 150);
        });
        input.addEventListener('keydown', function (event) {
            if (tokens && event.key === 'Backspace' && input.value === '') {
                const last = Array.from(tokens.querySelectorAll('.tok')).pop();
                if (last) removeToken(last);
                return;
            }
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
            if (event.target.name === 'confirm_actions') {
                if (event.target.checked) delete document.body.dataset.confirm; else document.body.dataset.confirm = 'off';
            }
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

    // steps move in the page; the form is saved as it is ordered when submitted
    document.querySelectorAll('ol[data-steps]').forEach(function (list) {
        const items = () => Array.from(list.children).filter((li) => li.classList.contains('step'));
        const prefix = /\bs\d+\./g;

        function renumber() {
            const all = items();
            // two passes so radio groups never share a name and lose their checked state
            ['tmp', 's'].forEach(function (pass) {
                all.forEach(function (li, i) {
                    li.querySelectorAll('[name]').forEach(function (el) {
                        el.name = el.name.replace(/^(?:tmp|s)\d+\./, pass + i + '.');
                    });
                });
            });
            all.forEach(function (li, i) {
                li.querySelector('.num').textContent = i + 1;
                li.querySelectorAll('*').forEach(function (el) {
                    ['id', 'for', 'list', 'aria-controls', 'aria-describedby', 'data-tokens'].forEach(function (a) {
                        const v = el.getAttribute(a);
                        if (v && prefix.test(v)) el.setAttribute(a, v.replace(prefix, 's' + i + '.'));
                        prefix.lastIndex = 0;
                    });
                    const label = el.getAttribute('aria-label');
                    if (label && /^(Move|Remove) step \d+/.test(label)) el.setAttribute('aria-label', label.replace(/step \d+/, 'step ' + (i + 1)));
                });
                li.querySelectorAll('button[name=op]').forEach(function (b) {
                    b.value = b.value.replace(/^(up|down|del):\d+$/, '$1:' + i).replace(/^tokdel:s\d+\./, 'tokdel:s' + i + '.');
                });
                li.querySelector('button[data-move="-1"]').disabled = i === 0;
                li.querySelector('button[data-move="1"]').disabled = i === all.length - 1;
            });
        }

        function moved(li) {
            renumber();
            li.classList.remove('moved');
            void li.offsetWidth;
            li.classList.add('moved');
            toast('Step moved to position ' + (items().indexOf(li) + 1) + '. Save to keep the order.');
        }

        list.addEventListener('click', function (event) {
            const button = event.target.closest('button[data-move]');
            if (!button) return;
            event.preventDefault();
            const li = button.closest('li.step');
            const all = items(), i = all.indexOf(li), j = i + Number(button.dataset.move);
            if (j < 0 || j >= all.length) return;
            const top = li.getBoundingClientRect().top;
            if (j < i) all[j].before(li); else all[j].after(li);
            moved(li);
            // keep the moved step under the pointer instead of the page jumping
            window.scrollBy(0, li.getBoundingClientRect().top - top);
            (button.disabled ? li.querySelector('button[data-move="' + -button.dataset.move + '"]') : button).focus();
        });

        let drag = null;
        list.addEventListener('pointerdown', function (event) {
            const handle = event.target.closest('.handle');
            if (!handle || event.button !== 0) return;
            const li = handle.closest('li.step');
            event.preventDefault();
            handle.setPointerCapture(event.pointerId);
            drag = { li, start: items().indexOf(li), y: event.clientY + window.scrollY, offset: li.offsetTop };
            li.classList.add('dragging');
            list.classList.add('drag-active');
        });
        // moving the step in the DOM drops its pointer capture, so the drag listens on the document
        document.addEventListener('pointermove', function (event) {
            if (!drag) return;
            const li = drag.li;
            for (const other of items()) {
                if (other === li) continue;
                const r = other.getBoundingClientRect(), mid = r.top + r.height / 2;
                const before = li.compareDocumentPosition(other) & Node.DOCUMENT_POSITION_PRECEDING;
                if (before && event.clientY < mid) { other.before(li); break; }
                if (!before && event.clientY > mid) { other.after(li); }
            }
            const shift = event.clientY + window.scrollY - drag.y - (li.offsetTop - drag.offset);
            li.style.translate = '0 ' + shift + 'px';
            const edge = 60;
            if (event.clientY < edge) window.scrollBy(0, -12); else if (event.clientY > window.innerHeight - edge) window.scrollBy(0, 12);
        });
        function drop() {
            if (!drag) return;
            const li = drag.li;
            li.style.translate = '';
            li.classList.remove('dragging');
            list.classList.remove('drag-active');
            if (items().indexOf(li) !== drag.start) moved(li);
            drag = null;
        }
        document.addEventListener('pointerup', drop);
        document.addEventListener('pointercancel', drop);
    });

    // editor buttons that post the form come back to the same scroll position
    document.querySelectorAll('form').forEach(function (form) {
        if (!form.querySelector('button[name=op]')) return;
        const key = 'scroll:' + location.pathname;
        try {
            const y = sessionStorage.getItem(key);
            if (y !== null) { sessionStorage.removeItem(key); window.scrollTo(0, Number(y)); }
        } catch (e) { /* storage blocked */ }
        form.addEventListener('submit', function (event) {
            const op = event.submitter && event.submitter.name === 'op' ? event.submitter.value : '';
            if (!op || op === 'save') return;
            try { sessionStorage.setItem(key, String(window.scrollY)); } catch (e) { /* storage blocked */ }
        });
    });

    // server choices of an access request: search and pages, without JavaScript all are listed
    document.querySelectorAll('[data-pick-list]').forEach(function (box) {
        const items = Array.from(box.querySelectorAll('[data-pick]'));
        const search = box.querySelector('[data-pick-search]');
        const pager = box.querySelector('[data-pick-pager]');
        const prev = pager.querySelector('[data-prev]');
        const next = pager.querySelector('[data-next]');
        const info = pager.querySelector('[data-page-text]');
        const picked = box.querySelector('[data-picked]');
        const send = box.form && box.form.querySelector('[data-pick-send]');
        const per = Number(box.dataset.pickList) || 8;
        let page = 0;
        function show() {
            const q = search.value.trim().toLowerCase();
            const match = items.filter((el) => !q || el.dataset.pick.toLowerCase().includes(q));
            const pages = Math.max(1, Math.ceil(match.length / per));
            page = Math.min(page, pages - 1);
            items.forEach((el) => { el.hidden = true; });
            match.slice(page * per, page * per + per).forEach((el) => { el.hidden = false; });
            info.textContent = match.length ? 'Page ' + (page + 1) + ' of ' + pages : 'No server matches.';
            prev.disabled = page === 0;
            next.disabled = page >= pages - 1;
            pager.hidden = pages === 1 && match.length > 0;
            search.hidden = items.length <= per;
        }
        function count() {
            const n = box.querySelectorAll('input[type=checkbox]:checked').length;
            picked.textContent = String(n);
            if (send) send.disabled = n === 0;
        }
        search.addEventListener('input', function () { page = 0; show(); });
        search.addEventListener('keydown', function (event) { if (event.key === 'Enter') event.preventDefault(); });
        prev.addEventListener('click', function () { page--; show(); });
        next.addEventListener('click', function () { page++; show(); });
        box.addEventListener('change', count);
        show();
        count();
    });
})();
