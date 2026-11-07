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
        const startMS = Number(btn.dataset.start || 0), endMS = Number(btn.dataset.end || 0);
        if (startMS) audio.currentTime = startMS / 1000;
        if (endMS) audio.addEventListener('timeupdate', function () { if (audio.currentTime * 1000 >= endMS && preview && preview.audio === audio) stopPreview(); });
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

    // a member's profile picture, or their initial without one
    function avatarOf(h, cls) {
        if (h.avatar) {
            const img = document.createElement('img');
            img.className = cls;
            img.src = h.avatar;
            img.alt = '';
            img.referrerPolicy = 'no-referrer';
            return img;
        }
        const av = document.createElement('span');
        av.className = cls;
        av.setAttribute('aria-hidden', 'true');
        av.textContent = (h.name.trim()[0] || '?').toUpperCase();
        return av;
    }

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
                tok.append(avatarOf(h, 'av'));
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
                if (h.username !== undefined) li.append(avatarOf(h, 'sav'));
                li.append(h.name);
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
            } else if (input.dataset.navigate) {
                input.value = h.name;
                location.href = input.dataset.navigate + encodeURIComponent(h.id);
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

    // the channel and member lists of the Control Room can be made wider or narrower
    const room = document.querySelector('.room');
    if (room) {
        const limits = { side: [180, 480], members: [180, 420] };
        const setWidth = function (which, px) {
            const [lo, hi] = limits[which];
            const w = Math.round(Math.min(hi, Math.max(lo, px)));
            room.style.setProperty('--' + which + '-w', w + 'px');
            try { localStorage.setItem('room-' + which, String(w)); } catch (e) { /* private mode */ }
            return w;
        };
        ['side', 'members'].forEach(function (which) {
            let saved = null;
            try { saved = localStorage.getItem('room-' + which); } catch (e) { /* private mode */ }
            if (saved) setWidth(which, Number(saved));
        });
        room.querySelectorAll('[data-resize]').forEach(function (handle) {
            const which = handle.dataset.resize;
            const panel = handle.parentElement;
            handle.setAttribute('aria-valuemin', limits[which][0]);
            handle.setAttribute('aria-valuemax', limits[which][1]);
            handle.addEventListener('pointerdown', function (event) {
                event.preventDefault();
                handle.setPointerCapture(event.pointerId);
                handle.classList.add('active');
                const startX = event.clientX, startW = panel.getBoundingClientRect().width;
                const move = function (e) {
                    const dx = e.clientX - startX;
                    handle.setAttribute('aria-valuenow', setWidth(which, which === 'side' ? startW + dx : startW - dx));
                };
                const up = function () {
                    handle.classList.remove('active');
                    handle.removeEventListener('pointermove', move);
                    handle.removeEventListener('pointerup', up);
                    handle.removeEventListener('pointercancel', up);
                };
                handle.addEventListener('pointermove', move);
                handle.addEventListener('pointerup', up);
                handle.addEventListener('pointercancel', up);
            });
            handle.addEventListener('keydown', function (event) {
                if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                event.preventDefault();
                const grow = (event.key === 'ArrowRight') === (which === 'side');
                const w = panel.getBoundingClientRect().width + (grow ? 16 : -16);
                handle.setAttribute('aria-valuenow', setWidth(which, w));
            });
        });
    }

    // the soundboard's play volume, saved for the user
    let playTimer;
    document.querySelectorAll('input[data-play-volume]').forEach(function (slider) {
        const out = slider.parentElement.querySelector('output');
        slider.addEventListener('input', function () {
            if (out) out.textContent = slider.value + '%';
            const csrf = document.querySelector('input[name=csrf]');
            if (!csrf) return;
            clearTimeout(playTimer);
            playTimer = setTimeout(function () {
                fetch('/prefs/play-volume', { method: 'POST', body: new URLSearchParams({ csrf: csrf.value, volume: slider.value }) })
                    .then((r) => { if (!r.ok) toast('Volume not saved, reload the page and try again'); }, () => toast('Volume not saved, check your connection'));
            }, 400);
        });
    });
    document.querySelectorAll('input[type=range][data-out]').forEach(function (slider) {
        const out = slider.parentElement.querySelector('output');
        slider.addEventListener('input', function () { out.textContent = slider.value + '%'; });
    });

    // Edit sound: waveform with draggable start and end, the number fields stay the source of truth
    document.querySelectorAll('[data-wave]').forEach(function (wave) {
        const form = wave.closest('form');
        const startIn = form.querySelector('input[data-trim=start]'), endIn = form.querySelector('input[data-trim=end]');
        const box = wave.querySelector('.wave-box'), canvas = box.querySelector('canvas');
        const sel = box.querySelector('.wave-sel'), pos = box.querySelector('.wave-pos');
        const playBtn = wave.querySelector('.wave-play');
        let total = Number(wave.dataset.duration) / 1000 || 0;
        let peaks = null, audio = null;
        const get = () => {
            const s = Math.max(0, Number(startIn.value) || 0);
            const e = Number(endIn.value) || total;
            return [s, Math.max(s, Math.min(e, total || e))];
        };
        function draw() {
            const w = box.clientWidth, h = box.clientHeight, dpr = window.devicePixelRatio || 1;
            canvas.width = w * dpr;
            canvas.height = h * dpr;
            const g = canvas.getContext('2d');
            g.scale(dpr, dpr);
            g.fillStyle = getComputedStyle(box).color;
            const bars = Math.floor(w / 3);
            for (let i = 0; i < bars; i++) {
                const p = peaks ? peaks[Math.floor(i / bars * peaks.length)] : 0.15;
                const bh = Math.max(2, p * (h - 8));
                g.fillRect(i * 3, (h - bh) / 2, 2, bh);
            }
            const [s, e] = get();
            if (total) {
                sel.style.left = (s / total * 100) + '%';
                sel.style.width = ((e - s) / total * 100) + '%';
            }
        }
        fetch(wave.dataset.wave).then((r) => r.arrayBuffer()).then(function (buf) {
            const Ctx = window.AudioContext || window.webkitAudioContext;
            const ctx = new Ctx();
            return ctx.decodeAudioData(buf).then(function (data) {
                ctx.close();
                total = total || data.duration;
                const ch = data.getChannelData(0), n = 400, step = Math.floor(ch.length / n) || 1;
                peaks = [];
                let maxPeak = 0;
                for (let i = 0; i < n; i++) {
                    let m = 0;
                    for (let j = i * step; j < Math.min(ch.length, (i + 1) * step); j += 16) m = Math.max(m, Math.abs(ch[j]));
                    peaks.push(m);
                    maxPeak = Math.max(maxPeak, m);
                }
                if (maxPeak > 0) peaks = peaks.map((p) => p / maxPeak);
                draw();
            });
        }).catch(function () { draw(); });
        const sheet = wave.closest('[popover]');
        if (sheet) sheet.addEventListener('toggle', () => requestAnimationFrame(draw));
        window.addEventListener('resize', draw);
        [startIn, endIn].forEach((el) => el.addEventListener('input', draw));
        let drag = null;
        box.addEventListener('pointerdown', function (event) {
            if (!total) return;
            const r = box.getBoundingClientRect();
            const t = (event.clientX - r.left) / r.width * total;
            const [s, e] = get();
            drag = Math.abs(t - s) <= Math.abs(t - e) ? startIn : endIn;
            box.setPointerCapture(event.pointerId);
            move(event);
        });
        function move(event) {
            if (!drag) return;
            const r = box.getBoundingClientRect();
            let t = Math.max(0, Math.min(total, (event.clientX - r.left) / r.width * total));
            const [s, e] = get();
            if (drag === startIn) t = Math.min(t, e - 0.05); else t = Math.max(t, s + 0.05);
            drag.value = t.toFixed(2);
            draw();
        }
        box.addEventListener('pointermove', move);
        box.addEventListener('pointerup', () => { drag = null; });
        box.addEventListener('pointercancel', () => { drag = null; });
        function stop() {
            if (!audio) return;
            audio.pause();
            audio = null;
            pos.hidden = true;
            playBtn.querySelector('use').setAttribute('href', '#i-play');
        }
        playBtn.addEventListener('click', function () {
            if (audio) { stop(); return; }
            const [s, e] = get();
            audio = new Audio(wave.dataset.wave);
            audio.volume = Number(document.body.dataset.previewVol || 100) / 100;
            audio.currentTime = s;
            playBtn.querySelector('use').setAttribute('href', '#i-square');
            pos.hidden = false;
            audio.addEventListener('timeupdate', function () {
                if (!audio) return;
                if (total) pos.style.left = (audio.currentTime / total * 100) + '%';
                if (audio.currentTime >= e) stop();
            });
            audio.addEventListener('ended', stop);
            audio.play().catch(function () { stop(); toast('The preview could not be played'); });
        });
        draw();
    });

    // emoji picker: the server's own emojis and the standard ones, searchable by name
    let emojiData = null;
    const loadEmojis = () => emojiData || (emojiData = fetch('/static/emoji.json').then((r) => r.json()).catch(() => []));
    const customRe = /^<(a?):([A-Za-z0-9_~]+):(\d+)>$/;
    const customURL = (m) => 'https://cdn.discordapp.com/emojis/' + m[3] + '.' + (m[1] ? 'gif' : 'png') + '?size=48';
    document.querySelectorAll('input[data-emoji-picker]').forEach(function (input, n) {
        let custom = [];
        try { custom = JSON.parse(input.dataset.emojiPicker || '[]') || []; } catch (e) { custom = []; }
        input.type = 'hidden';
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'emoji-btn';
        btn.setAttribute('aria-haspopup', 'dialog');
        const label = input.getAttribute('aria-labelledby');
        if (label) btn.setAttribute('aria-labelledby', label + ' emoji-now-' + n);
        const panel = document.createElement('div');
        panel.className = 'emoji-panel';
        panel.hidden = true;
        panel.setAttribute('role', 'dialog');
        panel.setAttribute('aria-label', 'Pick an emoji');
        const search = document.createElement('input');
        search.type = 'search';
        search.placeholder = 'Search emojis, like fire or salt';
        search.setAttribute('aria-label', 'Search emojis');
        search.autocomplete = 'off';
        const list = document.createElement('div');
        list.className = 'emoji-list';
        const none = document.createElement('button');
        none.type = 'button';
        none.className = 'ib sm quiet with-label';
        none.innerHTML = '<span class="lbl">No emoji</span>';
        panel.append(search, list, none);
        input.after(btn, panel);
        function show() {
            btn.textContent = '';
            const v = input.value, m = v.match(customRe);
            const now = document.createElement('span');
            now.id = 'emoji-now-' + n;
            if (m) {
                const img = document.createElement('img');
                img.src = customURL(m);
                img.alt = '';
                img.referrerPolicy = 'no-referrer';
                btn.append(img);
                now.textContent = ':' + m[2] + ':';
            } else if (v) {
                const e = document.createElement('span');
                e.className = 'emoji-char';
                e.textContent = v;
                btn.append(e);
                now.textContent = v;
            } else {
                btn.append(svgIcon('smile'));
                now.textContent = 'Click to select';
            }
            btn.append(now);
        }
        function pick(value) {
            input.value = value;
            show();
            close();
            btn.focus();
        }
        function cell(text, title, value, img) {
            const b = document.createElement('button');
            b.type = 'button';
            b.title = title;
            b.setAttribute('aria-label', title);
            if (img) {
                const i = document.createElement('img');
                i.src = img;
                i.alt = '';
                i.loading = 'lazy';
                i.referrerPolicy = 'no-referrer';
                b.append(i);
            } else {
                b.textContent = text;
            }
            b.addEventListener('click', () => pick(value));
            return b;
        }
        function render(groups) {
            const q = search.value.trim().toLowerCase().replace(/^:|:$/g, '');
            list.textContent = '';
            let shown = 0;
            const section = function (name, cells) {
                if (!cells.length) return;
                const h = document.createElement('span');
                h.className = 'eyebrow';
                h.textContent = name;
                const grid = document.createElement('div');
                grid.className = 'emoji-grid';
                grid.append(...cells);
                list.append(h, grid);
                shown += cells.length;
            };
            section('This server', custom.filter((c) => !q || c.Name.toLowerCase().includes(q))
                .map((c) => cell('', ':' + c.Name + ':', '<:' + c.Name + ':' + c.ID + '>', c.URL)));
            (groups || []).forEach(function (g) {
                if (shown > 400) return;
                section(g.g, g.e.filter((e) => !q || e[1].includes(q)).slice(0, q ? 200 : 1000).map((e) => cell(e[0], e[1], e[0])));
            });
            if (!shown) {
                const p = document.createElement('p');
                p.className = 'hint';
                p.textContent = 'No emoji matches "' + search.value + '".';
                list.append(p);
            }
        }
        function open() {
            panel.hidden = false;
            btn.setAttribute('aria-expanded', 'true');
            search.value = '';
            render(null);
            loadEmojis().then(render);
            search.focus();
        }
        function close() {
            panel.hidden = true;
            btn.setAttribute('aria-expanded', 'false');
        }
        btn.addEventListener('click', () => (panel.hidden ? open() : close()));
        none.addEventListener('click', () => pick(''));
        search.addEventListener('input', () => loadEmojis().then(render));
        search.addEventListener('keydown', function (event) {
            if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); btn.focus(); }
            if (event.key === 'Enter') {
                event.preventDefault();
                const first = list.querySelector('.emoji-grid button');
                if (first) first.click();
            }
        });
        document.addEventListener('click', function (event) {
            if (!panel.hidden && !panel.contains(event.target) && event.target !== btn && !btn.contains(event.target)) close();
        });
        show();
    });

    // role colors of member names, set here because the CSP allows no inline styles
    document.querySelectorAll('[data-color]').forEach(function (el) {
        if (/^#[0-9a-f]{6}$/i.test(el.dataset.color)) el.style.color = el.dataset.color;
    });

    // servers in the user's order: drag in the server list or the Control Room rail
    document.querySelectorAll('[data-sortable]').forEach(function (box) {
        const items = () => Array.from(box.querySelectorAll(':scope > [data-sort-id]'));
        const handles = box.querySelector('.sort-handle') !== null;
        let drag = null, justDragged = false;
        function save() {
            const csrf = document.querySelector('input[name=csrf]');
            if (!csrf) return;
            const body = new URLSearchParams({ csrf: csrf.value });
            items().forEach((el) => body.append('id', el.dataset.sortId));
            fetch(box.dataset.sortable, { method: 'POST', body, headers: { Accept: 'application/json' } })
                .then((r) => toast(r.ok ? 'Order saved' : 'Order not saved, reload the page and try again'), () => toast('Order not saved, check your connection'));
        }
        box.addEventListener('pointerdown', function (event) {
            if (event.button !== 0) return;
            const el = event.target.closest('[data-sort-id]');
            if (!el || el.parentElement !== box || (handles && !event.target.closest('.sort-handle'))) return;
            const all = items();
            const horizontal = all.length > 1 && Math.abs(all[1].offsetTop - all[0].offsetTop) < 4;
            drag = { el, x: event.clientX, y: event.clientY, horizontal, moving: false, start: all.indexOf(el), id: event.pointerId };
            if (handles) event.preventDefault();
        });
        document.addEventListener('pointermove', function (event) {
            if (!drag || event.pointerId !== drag.id) return;
            const d = drag.horizontal ? event.clientX - drag.x : event.clientY - drag.y;
            if (!drag.moving) {
                if (Math.abs(d) < 6) return;
                drag.moving = true;
                drag.el.classList.add('sorting');
                box.classList.add('sort-active');
            }
            drag.el.style.translate = drag.horizontal ? d + 'px 0' : '0 ' + d + 'px';
            const r = drag.el.getBoundingClientRect();
            const mid = drag.horizontal ? r.left + r.width / 2 : r.top + r.height / 2;
            const prev = drag.el.previousElementSibling, next = drag.el.nextElementSibling;
            const center = (el) => { const b = el.getBoundingClientRect(); return drag.horizontal ? b.left + b.width / 2 : b.top + b.height / 2; };
            if (prev && prev.dataset.sortId && mid < center(prev)) {
                const before = drag.el.getBoundingClientRect();
                prev.before(drag.el);
                const after = drag.el.getBoundingClientRect();
                if (drag.horizontal) drag.x += after.left - before.left; else drag.y += after.top - before.top;
            } else if (next && next.dataset.sortId && mid > center(next)) {
                const before = drag.el.getBoundingClientRect();
                next.after(drag.el);
                const after = drag.el.getBoundingClientRect();
                if (drag.horizontal) drag.x += after.left - before.left; else drag.y += after.top - before.top;
            }
            const now = drag.horizontal ? event.clientX - drag.x : event.clientY - drag.y;
            drag.el.style.translate = drag.horizontal ? now + 'px 0' : '0 ' + now + 'px';
        });
        function drop() {
            if (!drag) return;
            const moved = drag.moving && items().indexOf(drag.el) !== drag.start;
            justDragged = drag.moving;
            drag.el.style.translate = '';
            drag.el.classList.remove('sorting');
            box.classList.remove('sort-active');
            drag = null;
            if (moved) save();
            setTimeout(() => { justDragged = false; }, 0);
        }
        document.addEventListener('pointerup', drop);
        document.addEventListener('pointercancel', drop);
        box.addEventListener('click', function (event) {
            if (justDragged) { event.preventDefault(); event.stopPropagation(); }
        }, true);
        box.addEventListener('dragstart', (event) => event.preventDefault());
    });
})();
