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
        btn.querySelector('use').setAttribute('href', playing ? '#i-square' : '#i-' + (btn.dataset.icon || 'play'));
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
    function saveVolume(volume) {
        const csrf = document.querySelector('input[name=csrf]');
        if (!csrf) return;
        clearTimeout(volTimer);
        volTimer = setTimeout(function () {
            fetch('/prefs/preview-volume', {
                method: 'POST',
                body: new URLSearchParams({ csrf: csrf.value, volume }),
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
        text.textContent = 'Preview volume, for all sounds';
        const slider = document.createElement('input');
        slider.type = 'range';
        slider.min = '0';
        slider.max = '100';
        slider.value = document.body.dataset.previewVol || '100';
        const out = document.createElement('output');
        out.textContent = slider.value + '%';
        label.append(text, slider);
        vol.append(label, out);
        audio.volume = Number(slider.value) / 100;
        if (btn.hasAttribute('data-no-slider')) {
            setPreviewButton(btn, true);
            preview = { audio, btn, vol, detail: null };
            audio.addEventListener('ended', function () { if (preview && preview.audio === audio) stopPreview(); });
            audio.play().catch(function () {
                if (preview && preview.audio === audio) stopPreview();
                toast('The preview could not be played');
            });
            return;
        }
        slider.addEventListener('input', function () {
            audio.volume = Number(slider.value) / 100;
            out.textContent = slider.value + '%';
            document.body.dataset.previewVol = slider.value;
            saveVolume(slider.value);
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
                pv.dataset.name = h.name;
                pv.setAttribute('aria-label', 'Preview ' + h.name);
                pv.setAttribute('aria-pressed', 'false');
                pv.dataset.tip = 'Preview ' + h.name;
                pv.append(svgIcon('play'));
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
        // sound pickers list the server's sounds before the first keystroke
        const browse = Boolean(tokens && tokens.dataset.source);
        function search(q, wait) {
            clearTimeout(timer);
            timer = setTimeout(function () {
                const mine = ++seq;
                const params = new URLSearchParams({ q });
                const guild = input.dataset.members ?? (tokens && tokens.dataset.guild);
                if (guild !== undefined) params.set('guild', guild);
                fetch(source + '?' + params).then((r) => r.ok ? r.json() : []).then(function (data) {
                    if (mine !== seq) return;
                    hits = tokens && tokens.dataset.dupes === undefined ? data.filter((h) => !picked().includes(String(h.id))) : data;
                    active = hits.length ? 0 : -1;
                    show();
                }).catch(() => {});
            }, wait);
        }
        input.addEventListener('input', function () {
            const q = input.value.trim();
            if (q === '' && !browse) { clearTimeout(timer); close(); return; }
            search(q, 150);
        });
        if (browse) input.addEventListener('focus', function () {
            if (input.value.trim() === '') search('', 0);
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

    // Discord markdown for the message previews, everything is escaped before marks become tags
    const escapeHTML = (s) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
    function markdown(src, names) {
        const held = [];
        const hold = (html) => { held.push(html); return '\u0000' + (held.length - 1) + '\u0000'; };
        src = src.replace(/\\([*_~|`>#\-\\\[\]()])/g, (_, c) => hold(escapeHTML(c)));
        src = src.replace(/```(?:[a-z0-9+-]*\n)?([\s\S]*?)```/gi, (_, code) => hold('<pre class="dpre">' + escapeHTML(code.replace(/\n$/, '')) + '</pre>'));
        function inline(s) {
            s = s.replace(/`([^`\n]+)`/g, (_, c) => hold('<code class="dcode">' + escapeHTML(c) + '</code>'));
            s = escapeHTML(s);
            s = s.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, t, u) => hold('<a class="dlink" href="' + u + '" target="_blank" rel="noopener">' + t + '</a>'));
            s = s.replace(/(^|[\s(])(https?:\/\/[^\s<]+)/g, (_, pre, u) => pre + hold('<a class="dlink" href="' + u + '" target="_blank" rel="noopener">' + u + '</a>'));
            s = s.replace(/&lt;@!?(\d+)&gt;/g, (_, id) => '<span class="dmention">@' + escapeHTML((names && names[id]) || id) + '</span>')
                .replace(/&lt;@&amp;(\d+)&gt;/g, '<span class="dmention">@role</span>')
                .replace(/&lt;#(\d+)&gt;/g, '<span class="dmention">#channel</span>')
                .replace(/\*\*\*([^*\n]+?)\*\*\*/g, '<b><i>$1</i></b>')
                .replace(/\*\*([^*\n]+?)\*\*/g, '<b>$1</b>')
                .replace(/__([^_\n]+?)__/g, '<u>$1</u>')
                .replace(/\*([^*\s][^*\n]*?)\*/g, '<i>$1</i>')
                .replace(/(^|[^\w])_([^_\n]+?)_(?![\w])/g, '$1<i>$2</i>')
                .replace(/~~([^~\n]+?)~~/g, '<s>$1</s>')
                .replace(/\|\|([^|\n]+?)\|\|/g, '<span class="dspoiler" tabindex="0">$1</span>');
            return s;
        }
        let quoteRest = false;
        const out = src.split('\n').map(function (line) {
            let m;
            if (!quoteRest && (m = line.match(/^>>> ?(.*)$/))) { quoteRest = true; line = m[1]; }
            if (quoteRest) return '<div class="dquote">' + (inline(line) || '&nbsp;') + '</div>';
            if ((m = line.match(/^> ?(.*)$/))) return '<div class="dquote">' + (inline(m[1]) || '&nbsp;') + '</div>';
            if ((m = line.match(/^(#{1,3}) (.+)$/))) return '<div class="dh' + m[1].length + '">' + inline(m[2]) + '</div>';
            if ((m = line.match(/^-# (.+)$/))) return '<div class="dsub">' + inline(m[1]) + '</div>';
            if ((m = line.match(/^\s*[-*] (.+)$/))) return '<div class="dli">' + inline(m[1]) + '</div>';
            if ((m = line.match(/^\s*(\d+)\. (.+)$/))) return '<div class="dli num"><span>' + m[1] + '.</span>' + inline(m[2]) + '</div>';
            return '<div class="dline">' + inline(line) + '</div>';
        }).join('');
        let html = out;
        for (let i = 0; i < 3 && html.includes('\u0000'); i++) html = html.replace(/\u0000(\d+)\u0000/g, (_, i) => held[Number(i)]);
        return html;
    }
    function namesOf(el) {
        const box = el.closest('[data-names]');
        try { return box ? JSON.parse(box.dataset.names) : {}; } catch (e) { return {}; }
    }
    function renderMarkdown(el, text) {
        el.classList.add('md');
        el.innerHTML = markdown(text, namesOf(el));
    }
    document.querySelectorAll('[data-md]').forEach((el) => renderMarkdown(el, el.dataset.md));

    // live preview of the message editor and the quick message box
    document.querySelectorAll('form[data-preview-form]').forEach(function (previewForm) {
        previewForm.addEventListener('input', function (event) {
            const key = event.target.dataset.preview;
            if (!key) return;
            if (key.startsWith('bstyle-') || key.startsWith('bkind-')) {
                const i = key.split('-')[1];
                const ed = event.target.closest('.button-ed');
                const link = ed && ed.querySelector('input[value=link]').checked;
                const btn = document.querySelector('[data-preview-target="bstyle-' + i + '"]');
                if (btn) btn.dataset.style = link ? 'link' : ed.querySelector('select[name$=".style"]').value;
                return;
            }
            const target = document.querySelector('[data-preview-target="' + key + '"]');
            if (!target) return;
            if (key.startsWith('color-')) {
                target.setAttribute('fill', event.target.value);
                return;
            }
            const link = target.querySelector('a');
            if (target.hasAttribute('data-md')) renderMarkdown(target, event.target.value);
            else (link || target).textContent = event.target.value;
            const counter = document.querySelector('[data-count="' + key + '"]');
            if (counter) {
                const n = [...event.target.value].length;
                counter.textContent = n + ' / ' + counter.dataset.max;
                counter.classList.toggle('over', n > Number(counter.dataset.max));
            }
        });
    });

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

    // one preview volume for every sound, e.g. on the soundboard
    document.querySelectorAll('input[data-preview-volume]').forEach(function (slider) {
        const out = slider.parentElement.querySelector('output');
        slider.addEventListener('input', function () {
            document.body.dataset.previewVol = slider.value;
            if (out) out.textContent = slider.value + '%';
            if (preview) preview.audio.volume = Number(slider.value) / 100;
            saveVolume(slider.value);
        });
    });

    // forms that act without leaving the page, like playing a sound or starring it
    document.addEventListener('submit', function (event) {
        const form = event.target.closest('form[data-fetch]');
        if (!form || event.defaultPrevented || !window.fetch) return;
        event.preventDefault();
        const body = new URLSearchParams(new FormData(form, event.submitter));
        const menu = form.closest('.menu[popover]');
        if (menu && menu.matches(':popover-open')) menu.hidePopover();
        fetch(form.action, { method: 'POST', body }).then(function (r) {
            if (!r.ok) throw new Error(String(r.status));
            if (form.dataset.done) toast(form.dataset.done);
            if (form.hasAttribute('data-toggle')) {
                const on = form.querySelector('input[name=on]');
                const btn = form.querySelector('[aria-pressed]');
                const now = on.value === '1';
                btn.setAttribute('aria-pressed', String(now));
                on.value = now ? '0' : '1';
                toast(now ? 'Added to Favorites' : 'Removed from Favorites');
            }
        }).catch(function () { toast('That did not work. Reload the page and try again.'); });
    });

    // menus of channels, members and sounds: right click or the ... button, arrow keys move
    function menuItems(menu) {
        return Array.from(menu.querySelectorAll('a[role=menuitem], button[role=menuitem]')).filter((el) => !el.hidden);
    }
    function placeMenu(menu, x, y) {
        menu.style.margin = '0';
        menu.style.inset = 'auto';
        const w = menu.offsetWidth, h = menu.offsetHeight;
        menu.style.left = Math.max(8, Math.min(x, window.innerWidth - w - 8)) + 'px';
        menu.style.top = (y + h > window.innerHeight - 8 ? Math.max(8, y - h) : y) + 'px';
        const first = menuItems(menu)[0];
        if (first) first.focus();
    }
    document.addEventListener('contextmenu', function (event) {
        const row = event.target.closest('[data-menu]');
        const menu = row && document.getElementById(row.dataset.menu);
        if (!menu || !menu.showPopover) return;
        event.preventDefault();
        let x = event.clientX, y = event.clientY;
        if (!x && !y) {
            const r = row.getBoundingClientRect();
            x = r.left + 16;
            y = r.bottom;
        }
        if (!menu.matches(':popover-open')) menu.showPopover();
        placeMenu(menu, x, y);
    });
    document.addEventListener('click', function (event) {
        const btn = event.target.closest('button[popovertarget]');
        const menu = btn && document.getElementById(btn.getAttribute('popovertarget'));
        if (!menu || !menu.classList.contains('menu')) return;
        requestAnimationFrame(function () {
            if (!menu.matches(':popover-open')) return;
            const r = btn.getBoundingClientRect();
            placeMenu(menu, r.left, r.bottom + 4);
        });
    });
    document.addEventListener('keydown', function (event) {
        const menu = event.target.closest && event.target.closest('.menu[popover]');
        if (!menu || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
        const items = menuItems(menu);
        let i = items.indexOf(document.activeElement);
        if (event.key === 'ArrowDown') i = (i + 1) % items.length;
        else if (event.key === 'ArrowUp') i = (i - 1 + items.length) % items.length;
        else i = event.key === 'Home' ? 0 : items.length - 1;
        event.preventDefault();
        items[i].focus();
    });
})();
