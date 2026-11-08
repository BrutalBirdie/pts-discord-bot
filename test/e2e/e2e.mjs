// Browser steps of the end-to-end test, driven by run.sh.
import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = 'http://localhost:8000';
const step = process.argv[2];
let failures = 0;

function check(name, ok, detail = '') {
    console.log(`${ok ? 'PASS' : 'FAIL'} [${step}] ${name}${!ok && detail ? ' :: ' + detail : ''}`);
    if (!ok) failures++;
}

const browser = await chromium.launch();
const consoleErrors = [];
let lastPage = null;

async function session(user) {
    const file = `/work/${user}.json`;
    const ctx = await browser.newContext({ storageState: fs.existsSync(file) ? file : undefined });
    const page = await ctx.newPage();
    // error pages answer with 4xx, the browser logs that as a failed resource
    page.on('console', (m) => { if (m.type() === 'error' && !/status of 4\d\d/.test(m.text())) consoleErrors.push(m.text()); });
    page.on('pageerror', (e) => consoleErrors.push(e.message));
    lastPage = page;
    return { ctx, page, save: () => ctx.storageState({ path: file }) };
}

async function login(page, user, groups) {
    await page.goto(BASE + '/');
    if (await page.locator('#app').count()) return;
    await page.getByRole('link', { name: 'Log in with Cloudron' }).click();
    await page.fill('input[name=username]', user);
    await page.fill('textarea[name=claims]', JSON.stringify({ groups, name: user }));
    await page.click('input[type=submit]');
    await page.waitForURL(BASE + '/');
}

// pages load in place; settle waits until the page shown last has arrived
const settle = (page) => page.waitForFunction(() => !document.documentElement.classList.contains('loading'));
const text = async (page) => { await settle(page); return page.locator('main').innerText(); };

// 1x1 PNG
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');

// a short 8 kHz mono WAV
const wav = Buffer.alloc(44 + 8000);
wav.write('RIFF', 0); wav.writeUInt32LE(36 + 8000, 4); wav.write('WAVEfmt ', 8); wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20);
wav.writeUInt16LE(1, 22); wav.writeUInt32LE(8000, 24); wav.writeUInt32LE(16000, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34);
wav.write('data', 36); wav.writeUInt32LE(8000, 40);

// zipOf stores files in a zip archive without compression
// https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT
const crcTable = Array.from({ length: 256 }, (_, n) => { for (let k = 0; k < 8; k++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1; return n >>> 0; });
const crc32 = (buf) => { let c = 0xffffffff; for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
function zipOf(files) {
    const parts = [], central = [];
    let offset = 0;
    for (const [name, data] of Object.entries(files)) {
        const n = Buffer.from(name), crc = crc32(data);
        const local = Buffer.alloc(30);
        local.writeUInt32LE(0x04034b50, 0); local.writeUInt16LE(20, 4);
        local.writeUInt32LE(crc, 14); local.writeUInt32LE(data.length, 18); local.writeUInt32LE(data.length, 22); local.writeUInt16LE(n.length, 26);
        const cen = Buffer.alloc(46);
        cen.writeUInt32LE(0x02014b50, 0); cen.writeUInt16LE(20, 4); cen.writeUInt16LE(20, 6);
        cen.writeUInt32LE(crc, 16); cen.writeUInt32LE(data.length, 20); cen.writeUInt32LE(data.length, 24); cen.writeUInt16LE(n.length, 28); cen.writeUInt32LE(offset, 42);
        parts.push(local, n, data);
        central.push(cen, n);
        offset += 30 + n.length + data.length;
    }
    const dir = Buffer.concat(central), end = Buffer.alloc(22), count = central.length / 2;
    end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(count, 8); end.writeUInt16LE(count, 10); end.writeUInt32LE(dir.length, 12); end.writeUInt32LE(offset, 16);
    return Buffer.concat([...parts, dir, end]);
}

// pickFiles uploads with the one button of the media page, which sends right after the pick
async function pickFiles(page, files) {
    await page.setInputFiles('form[data-upload] input[type=file]', files);
    await page.waitForURL(/done=uploaded/);
}

// until polls a condition that a live update makes true
async function until(fn, ms = 6000) {
    const end = Date.now() + ms;
    for (;;) {
        try { if (await fn()) return true; } catch (e) { /* not there yet */ }
        if (Date.now() > end) return false;
        await new Promise((r) => setTimeout(r, 100));
    }
}

const steps = {
    async admin() {
        const { page, save } = await session('ann');
        await page.goto(BASE + '/');
        check('no Discord login without a client secret', await page.locator('a[href="/login/discord"]').count() === 0
            && (await page.request.get(BASE + '/login/discord', { maxRedirects: 0 })).status() === 404);
        await login(page, 'ann', ['bot-admins']);
        await save();
        let t = await text(page);
        check('Home overview renders', t.includes('Overview') && t.includes('Next 24 hours'));
        check('offline notice', t.includes('The bot is not connected to Discord') && t.includes('DISCORD_TOKEN is not set'), t.slice(0, 300));
        check('frame with rail, Home list and user panel', await page.locator('.rail a[href="/"][aria-current=page]').count() === 1
            && await page.locator('.side-body a[href="/trash"]').count() === 1 && await page.locator('.side-foot a[href="/settings"]').count() === 1);
        await page.goto(BASE + '/settings');
        check('admin link for admin group in the settings', await page.locator('.set-nav a[href="/admin"]').count() === 1);

        await page.goto(BASE + '/room');
        check('Control Room without servers', (await text(page)).includes('The bot is in no server you may manage yet.')
            && (await page.locator('.side-body').innerText()).includes('offline, or in none yet'));

        await page.goto(BASE + '/admin');
        check('admin page shows group', (await text(page)).includes('bot-admins'));
        await page.goto(BASE + '/admin/settings');
        await page.evaluate(() => { window.stayed = true; });
        await page.fill('input[name=max_upload_mb]', '50');
        await page.click('main form button[type=submit]');
        check('settings saved', (await text(page)).includes('Saved.') && await page.inputValue('input[name=max_upload_mb]') === '50');
        check('a notice leaves the address', !page.url().includes('done='), page.url());
        check('saving does not load a new page', await page.evaluate(() => window.stayed === true));
        await page.click('.notice .dismiss');
        check('a notice can be dismissed', await page.locator('.notice').count() === 0);
        await page.reload();
        check('a reload does not bring the notice back', await page.locator('.notice').count() === 0);
        await page.evaluate(() => { window.stayed = true; });
        await page.click('.set-nav a[href="/admin/audit"]');
        await settle(page);
        check('navigation shows the page in place', page.url() === BASE + '/admin/audit' && await page.evaluate(() => window.stayed === true)
            && await page.locator('.set-nav a[href="/admin/audit"][aria-current=page]').count() === 1 && (await page.title()).length > 0);
        await page.click('.set-close');
        await settle(page);
        check('closing the settings goes Home in place', page.url() === BASE + '/' && await page.evaluate(() => window.stayed === true) && await page.locator('.rail').count() === 1);
        await page.goBack();
        await settle(page);
        await page.goBack();
        await settle(page);
        check('Back shows the page before in place', page.url().endsWith('/admin/settings') && await page.evaluate(() => window.stayed === true)
            && await page.locator('input[name=max_upload_mb]').count() === 1);

        await page.goto(BASE + '/media');
        t = await text(page);
        check('imported sound listed', t.includes('beep.ogg') && /0:\d\d/.test(t), t.slice(0, 500));
        check('upload limit from settings', t.includes('up to 50 MB'));
        await pickFiles(page, { name: 'friday card.png', mimeType: 'image/png', buffer: png });
        t = await text(page);
        check('image uploaded', t.includes('Uploaded.') && t.includes('friday_card.png') && t.includes('1 × 1'), t.slice(0, 500));
        check('a single upload opens its Edit sheet', await page.locator('[data-edit-sheet]').isVisible());
        await page.goto(BASE + '/media');
        const img = await page.request.get(BASE + (await page.locator('img[src^="/media/"]').first().getAttribute('src')));
        check('image served inline', img.ok() && img.headers()['content-type'] === 'image/png');
        const tile = page.locator('button.preview-tile').first();
        const audio = await page.request.get(BASE + '/media/' + await tile.getAttribute('data-preview-media') + '/file', { headers: { Range: 'bytes=0-3' } });
        check('sound supports range requests', audio.status() === 206 && (await audio.body()).toString() === 'OggS');
        check('sound rows have a preview tile, no player', await page.locator('audio').count() === 0 && await tile.getAttribute('aria-pressed') === 'false');
        const soundRow = page.locator('main .list > li', { has: tile });
        await tile.click();
        const tileSlider = soundRow.locator('.pv-vol input[type=range]');
        check('preview tile plays and shows the volume', await tile.getAttribute('aria-pressed') === 'true' && await tile.getAttribute('aria-label') === 'Stop beep.ogg'
            && await tileSlider.isVisible() && await soundRow.locator('[data-preview-detail]').isHidden()
            && await soundRow.locator('.pv-vol output').innerText() === '100%' && await soundRow.getByLabel('Preview volume').count() === 1);
        await tile.click();
        check('preview tile stops and restores the detail', await tile.getAttribute('aria-pressed') === 'false' && await tileSlider.count() === 0
            && await soundRow.locator('[data-preview-detail]').isVisible());

        await page.goto(BASE + '/messages/new');
        await page.fill('input[name=name]', 'Friday reminder');
        await page.fill('textarea[name=content]', 'Jumu’ah Mubarak {date}');
        await page.click('button[value=add-embed]');
        check('editor op keeps unsaved state', (await text(page)).includes('Changes are not saved yet') && await page.inputValue('input[name=name]') === 'Friday reminder');
        await page.fill('input[name="e0.title"]', 'Friday prayer');
        check('live preview', (await page.locator('[data-preview-target="title-0"]').innerText()) === 'Friday prayer');
        check('mention picker shown with JS', await page.locator('input[data-insert=content]').isVisible());
        await page.route('**/members?*', (route) => route.fulfill({ json: [{ id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' }] }));
        await page.locator('textarea[name=content]').evaluate((a) => a.setSelectionRange(a.value.length, a.value.length));
        await page.locator('input[data-insert=content]').pressSequentially('ja');
        await page.locator('.suggestions li').first().click();
        check('mention inserted into the text', (await page.inputValue('textarea[name=content]')).endsWith('<@111111111111111111>'));
        await page.unroute('**/members?*');
        await page.locator('.imgopt:has(input[name="e0.image"])', { hasText: 'friday_card.png' }).click();
        check('the image picker shows thumbnails and marks the pick', await page.locator('.imgopt:has(input[name="e0.image"]:checked) img[src^="/media/"]').count() === 1);
        await page.click('button[value="add-field:0"]');
        await page.fill('input[name="e0.f0.name"]', 'Khutbah');
        await page.fill('input[name="e0.f0.value"]', '13:15');
        await page.click('main .btns button[value=save]');
        t = await text(page);
        check('message saved', t.includes('Saved.') && t.includes('Khutbah'), t.slice(0, 400));
        check('embed image in preview', await page.locator('.dembed img.dimg').count() === 1);
        check('placeholder expanded', /Mubarak \d{4}-\d\d-\d\d/.test(await page.locator('[data-preview-target=content]').innerText()));

        await page.goto(BASE + '/media');
        check('media shows usage', (await text(page)).includes('used by message Friday reminder'));
        await page.locator('main .list a[href*="edit="]').first().click();
        const editSheet = page.locator('#sheet-edit:popover-open');
        await editSheet.waitFor();
        const tagInput = editSheet.locator('[data-tokens=tag] input[type=search]');
        await tagInput.fill('Greeting');
        await editSheet.locator('.suggestions li', { hasText: 'new tag' }).waitFor();
        await tagInput.press('Enter');
        await tagInput.fill('funny');
        await editSheet.locator('.suggestions li', { hasText: 'new tag' }).waitFor();
        await tagInput.press('Enter');
        check('tags picked as chips', await editSheet.locator('[data-tokens=tag] .tok').count() === 2);
        const scrolled = await page.evaluate(() => { window.scrollTo(0, 400); return window.scrollY; });
        await editSheet.locator('.btns button[type=submit]').click();
        check('tags saved without a reload', await until(async () => await page.locator('.list a.chip', { hasText: 'greeting' }).count() === 1)
            && await page.evaluate(() => window.scrollY) === scrolled && !(await editSheet.isVisible()));
        await page.goto(BASE + '/media');
        await page.locator('main .list a[href*="edit="]').nth(1).click();
        await page.locator('#sheet-edit:popover-open').waitFor();
        const tagInput2 = page.locator('#sheet-edit:popover-open [data-tokens=tag] input[type=search]');
        await tagInput2.fill('gre');
        check('the tag picker suggests tags in use', await until(async () => (await page.locator('#sheet-edit:popover-open .suggestions li').first().innerText()).startsWith('greeting')));
        await page.keyboard.press('Escape');
        await page.goto(BASE + '/media');
        await page.click('.pills a:has-text("greeting")');
        check('tag filter in place', await until(async () => page.url().includes('tag=greeting') && (await page.locator('main .list > li').count()) === 1), page.url());
        await page.fill('main input[name=q]', 'zzz');
        check('search as you type keeps the tag filter', await until(async () => page.url().includes('q=zzz') && page.url().includes('tag=greeting') && (await text(page)).includes('Nothing matches "zzz".')), page.url());
        await page.goto(BASE + '/media?q=BEEP');
        t = await text(page);
        check('media search', await page.locator('main .list > li').count() === 1 && t.includes('beep.ogg') && await page.inputValue('main input[name=q]') === 'BEEP', t.slice(0, 300));
        check('filter pills keep the search', await page.locator('.pills a', { hasText: 'Sounds' }).getAttribute('href') === '/media?kind=sound&q=BEEP');
        await page.goto(BASE + '/media');
        await page.click('button[popovertarget=sheet-link]');
        await page.fill('input[name=url]', 'http://127.0.0.1:8000/healthz');
        await page.click('form[action="/media/url"] button[type=submit]');
        check('link to the app itself refused', (await text(page)).includes('is not a public address'), (await text(page)).slice(0, 300));

        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', 'Adhan');
        await page.check('input[name=kind][value=daily]', { force: true });
        await page.fill('input[name=times]', '18:15, 5:00');
        await page.click('button[value="add:play"]');
        await page.click('button[value="add:wait"]');
        await page.click('button[value="add:leave"]');
        const stepTypes = () => page.locator('input[name$=".type"]').evaluateAll((els) => els.map((e) => e.name + '=' + e.value).join());
        await page.evaluate(() => { window.notReloaded = true; });
        await page.click('button[value="up:2"]');
        check('steps added and moved', await stepTypes() === 's0.type=play,s1.type=leave,s2.type=wait', await stepTypes());
        check('moving does not reload the page', await page.evaluate(() => window.notReloaded === true));
        await page.focus('input[name="s0.find"]');
        const beepOption = page.locator('[role=option]', { hasText: 'beep.ogg' });
        await beepOption.first().waitFor({ timeout: 5000 }).catch(() => {});
        check('sound picker lists sounds before typing', await beepOption.count() > 0);
        await page.keyboard.press('Escape');
        check('moved step renumbered', await page.locator('.step').nth(1).locator('button[data-move="-1"]').getAttribute('value') === 'up:1'
            && await page.locator('.step').nth(1).locator('.num').innerText() === '2'
            && await page.locator('.step').nth(0).locator('button[data-move="-1"]').isDisabled());
        const handle = page.locator('.step').nth(2).locator('.handle').first();
        const target = await page.locator('.step').nth(0).boundingBox();
        const from = await handle.boundingBox();
        await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
        await page.mouse.down();
        for (let k = 1; k <= 10; k++) await page.mouse.move(from.x + from.width / 2, from.y + (target.y + 5 - from.y) * k / 10);
        await page.mouse.up();
        check('drag and drop moves a step', await stepTypes() === 's0.type=wait,s1.type=play,s2.type=leave', await stepTypes());
        await page.locator('.step').nth(0).locator('button[data-move="1"]').click();
        await page.locator('.step').nth(1).locator('button[data-move="1"]').click();
        check('down moves back', await stepTypes() === 's0.type=play,s1.type=leave,s2.type=wait' && await page.evaluate(() => window.notReloaded === true), await stepTypes());
        await page.click('button[value="add:dm"]');
        await page.fill('input[name="s3.user"]', 'nobody');
        await page.click('main .btns button[value=save]');
        t = await text(page);
        check('task needs a server', t.includes('Not saved.') && t.includes('Pick a server'), t.slice(0, 500));
        check('unknown member reported', t.includes('no member of this server matches "nobody"') && await page.inputValue('input[name="s3.user"]') === 'nobody');
        await page.route('**/members?*', (route) => route.fulfill({ json: [
            { id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' },
            { id: '222222222222222222', label: 'Amira (@amira)', name: 'Amira', username: 'amira' }] }));
        await page.fill('input[name="s3.user"]', '');
        await page.locator('input[name="s3.user"]').pressSequentially('ja');
        await page.locator('.suggestions li').first().waitFor();
        await page.keyboard.press('ArrowDown');
        await page.keyboard.press('Enter');
        check('picker fills the chosen member', await page.inputValue('input[name="s3.user"]') === 'Amira (@amira)');
        check('enter in picker does not submit', page.url().endsWith('/tasks/new') && (await text(page)).includes('Not saved.'));
        await page.unroute('**/members?*');
        const members = await page.request.get(BASE + '/members?q=a');
        check('member search answers JSON', members.ok() && Array.isArray(await members.json()));
        check('times normalized', await page.inputValue('input[name=times]') === '05:00 18:15');
        check('next runs preview', (await page.locator('aside .list .when').count()) === 5);

        const res = await page.request.post(BASE + '/tasks/new', { form: { op: 'save', name: 'x' } });
        check('post without csrf token rejected', res.status() === 403);

        await page.click('button[value="add:profile"]');
        const nick = page.locator('input[name="s4.nick"]');
        const hiddenFirst = await nick.isHidden();
        await page.selectOption('select[name="s4.nick_mode"]', 'set');
        check('profile step shows the nickname field when set', hiddenFirst && await nick.isVisible());
        check('profile step offers library images', (await page.locator('.imgopt:has(input[name="s4.avatar"])').allInnerTexts()).some((t) => t.includes('friday_card.png')));

        const throwaway = async (name) => {
            await page.goto(BASE + '/messages/new');
            await page.fill('input[name=name]', name);
            await page.fill('textarea[name=content]', 'x');
            await page.click('main .btns button[value=save]');
            return page.locator('form[action$="/delete"] button[data-confirm-desc]');
        };
        let del = await throwaway('Throwaway one');
        check('delete button follows the labels preference', await del.locator('.lbl').isHidden() && await page.locator('input[name=confirm]').count() === 0);
        await del.click();
        const line = page.locator('.confirm-line[role=status]');
        await line.getByText('Click again to confirm.').waitFor();
        check('first click arms the delete button', /\/messages\/\d+/.test(page.url()) && await del.evaluate((b) => b.classList.contains('armed'))
            && await del.locator('.lbl').isVisible() && await del.locator('.lbl').innerText() === 'Delete message?'
            && (await line.innerText()).includes('Moves the message to the Trash. Click again to confirm.'), await line.innerText());
        await page.keyboard.press('Escape');
        check('escape disarms', await line.count() === 0 && await del.locator('.lbl').isHidden() && await del.locator('.lbl').textContent() === 'Delete message');
        await del.click();
        await line.getByRole('button', { name: 'Cancel' }).click();
        check('cancel disarms', await line.count() === 0 && !(await del.evaluate((b) => b.classList.contains('armed'))));
        await del.click();
        await del.click();
        await page.waitForURL(/\/messages\?.*done=trashed/);
        check('second click deletes', !(await text(page)).includes('Throwaway one'));

        const toggleConfirm = async () => {
            await page.click('.side-foot a[href="/settings"]');
            await settle(page);
            const saved = page.waitForResponse((r) => r.url().endsWith('/account'));
            await page.locator('main input[name=confirm_actions]').click();
            check('account preference saved', (await saved).status() === 204);
            await page.keyboard.press('Escape');
            await settle(page);
        };
        await toggleConfirm();
        await page.reload();
        check('confirm actions off is kept', await page.evaluate(() => document.body.dataset.confirm) === 'off'
            && (await (await page.request.get(BASE + '/settings')).text()).includes('name="confirm_actions">'));
        del = await throwaway('Throwaway two');
        await del.click();
        await page.waitForURL(/\/messages\?.*done=trashed/);
        check('with confirm actions off one click deletes', !(await text(page)).includes('Throwaway two'));
        await toggleConfirm();
        check('confirm actions on again', await page.evaluate(() => document.body.dataset.confirm) === undefined);

        await page.goto(BASE + '/admin/audit');
        t = await text(page);
        check('audit log', ['media.upload', 'message.create', 'settings.update'].every((a) => t.includes(a)), t.slice(0, 500));
        await save();
    },

    async user() {
        const { page, save } = await session('bob');
        await login(page, 'bob', []);
        await page.goto(BASE + '/settings');
        check('no admin link', await page.locator('a[href="/admin"]').count() === 0);
        await page.goto(BASE + '/admin');
        check('admin page refused', (await text(page)).includes('Only admins'));
        await page.goto(BASE + '/messages');
        check('user sees all messages', (await text(page)).includes('Friday reminder'));
        await save();
    },

    // long names without spaces, like a server called openpgp4fpr:3EFABCB7..., must not widen the page
    async layout() {
        const { page } = await session('ann');
        const long = 'openpgp4fpr_3EFABCB708412AEE165EDC6C7B17D5A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F6';
        await page.goto(BASE + '/media');
        await pickFiles(page, { name: long + '.png', mimeType: 'image/png', buffer: png });
        await page.goto(BASE + '/messages/new');
        await page.fill('input[name=name]', long);
        await page.fill('textarea[name=content]', long + long);
        await page.click('main .btns button[value=save]');
        const msg = page.url();
        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', long);
        await page.click('button[value="add:message"]');
        const task = page.url();
        for (const width of [1280, 390]) {
            await page.setViewportSize({ width, height: 900 });
            for (const path of ['/media', '/messages', msg.replace(BASE, ''), '/trash', '/admin/settings', '/admin/audit']) {
                await page.goto(BASE + path);
                const wide = await page.evaluate(() => [...document.querySelectorAll('main *')]
                    .filter((el) => {
                        if (el.offsetParent === null || el.closest('.url, .tabs, [popover]')) return false;
                        const box = el.parentElement.closest('.card, .danger-box, .embed-ed, .step, .dpv, main');
                        return el.getBoundingClientRect().right > Math.min(box.getBoundingClientRect().right, window.innerWidth) + 1;
                    })
                    .map((el) => el.tagName.toLowerCase() + '.' + el.className).slice(0, 5));
                check(`no overflow on ${path.split('?')[0]} at ${width} px`, wide.length === 0, wide.join(', '));
            }
        }
        check('task editor reached', task.includes('/tasks/new'));
    },

    // posts the delete forms directly, so the test does not depend on the confirm UI
    async trash() {
        const { page } = await session('ann');
        const post = async (sel) => {
            await page.locator(sel).first().evaluate((f) => {
                const c = f.querySelector('input[name=confirm]');
                if (c) c.value = c.dataset.confirm;
                f.requestSubmit();
            });
            await settle(page);
        };
        const taskRow = () => page.locator('main .list > li', { has: page.getByRole('link', { name: 'Every minute', exact: true }) });

        const trashCount = async () => Number(await page.locator('.side-body a[href="/trash"] .badge').textContent({ timeout: 1000 }).catch(() => '0'));
        await page.goto(BASE + '/tasks');
        const before = await trashCount();
        await page.getByRole('link', { name: 'Every minute', exact: true }).click();
        await settle(page);
        const taskID = page.url().split('/').pop();
        await post('form[action$="/delete"]');
        let t = await text(page);
        check('task moved to the trash', t.includes('Moved to the Trash.') && await taskRow().count() === 0, t.slice(0, 300));
        check('nav shows the trash count', await trashCount() === before + 1);
        await post('.notice form.undo');
        check('undo brings the task back', (await text(page)).includes('stays paused') && await taskRow().count() === 1);
        await page.goto(BASE + '/tasks/' + taskID);
        await post('form[action$="/delete"]');
        await page.goto(BASE + '/trash');
        t = await text(page);
        check('task listed in the trash', t.includes('Every minute') && t.includes('removed in 30 days') && t.includes('Deleted by ann'), t.slice(0, 500));
        await post(`form[action="/trash/task/${taskID}/restore"]`);
        check('task restored', (await text(page)).includes('Restored.'));
        await page.goto(BASE + '/tasks');
        check('restored task is paused', (await taskRow().innerText()).includes('paused'), await taskRow().innerText());

        const upload = async () => {
            await page.goto(BASE + '/media');
            await pickFiles(page, { name: 'chime.wav', mimeType: 'audio/wav', buffer: wav });
            return (await text(page)).includes('Uploaded.');
        };
        const chimes = () => page.locator('main .list > li', { hasText: 'chime' });
        check('sound uploaded', await upload());
        check('a new sound opens Edit sound', /edit=\d+/.test(page.url()) && await page.locator('#sheet-edit').isVisible()
            && await page.locator('#sheet-edit input[name=trim_end]').inputValue() === '0.50', page.url());
        await page.locator('#sheet-edit .emoji-btn').click();
        await page.fill('#sheet-edit .emoji-panel input[type=search]', 'fire');
        await page.locator('#sheet-edit .emoji-grid button[title="fire"]').click();
        await page.fill('#sheet-edit input[name=trim_start]', '0.1');
        await page.click('#sheet-edit button[type=submit]');
        check('Edit sound saves emoji and start', await until(async () => await chimes().locator('.face', { hasText: '🔥' }).count() === 1));
        const action = await chimes().locator('form[action$="/delete"]').getAttribute('action');
        const mediaID = action.split('/')[2];
        await post(`form[action="${action}"]`);
        check('sound moved to the trash', (await text(page)).includes('Moved to the Trash.') && await chimes().count() === 0);
        check('same name uploads again', await upload());
        await page.goto(BASE + '/trash?kind=media');
        check('trash names the new name', (await text(page)).includes('comes back as chime (restored).wav'));
        await post(`form[action="/trash/media/${mediaID}/restore"]`);
        check('restore renames on a clash', (await text(page)).includes('Restored as chime (restored).wav'), (await text(page)).slice(0, 300));
        await page.goto(BASE + '/media');
        check('both sounds in media', await chimes().count() === 2);

        await post(`form[action="/media/${mediaID}/delete"]`);
        const bob = await session('bob');
        await bob.page.goto(BASE + '/trash');
        t = await bob.page.locator('main').innerText();
        check('user sees the trash without purge', t.includes('chime (restored).wav') && await bob.page.locator('form[action$="/purge"], form[action="/trash/empty"]').count() === 0, t.slice(0, 300));
        lastPage = page;
        await page.goto(BASE + '/trash');
        await post(`form[action="/trash/media/${mediaID}/purge"]`);
        t = await text(page);
        check('admin purges an item', t.includes('Deleted for good.') && !t.includes('chime (restored).wav'));
        check('purged file is gone', (await page.request.get(`${BASE}/media/${mediaID}/file`)).status() === 404);

        await page.goto(BASE + '/media');
        await post(`form[action="${await chimes().locator('form[action$="/delete"]').getAttribute('action')}"]`);
        await page.goto(BASE + '/trash');
        await post('form[action="/trash/empty"]');
        t = await text(page);
        check('trash emptied', t.includes('The trash is empty.') && await page.locator('.side-body a[href="/trash"] .badge').count() === 0, t.slice(0, 300));
        const csrf = await page.locator('input[name=csrf]').first().inputValue();
        const tile = await page.request.post(BASE + '/media', { multipart: { csrf, next: '/servers?x=1', file: { name: 'tile.wav', mimeType: 'audio/wav', buffer: wav } } });
        check('an upload from the soundboard returns there with Edit sound', tile.ok() && /\/servers\?x=1&edit=\d+&done=uploaded/.test(tile.url()), tile.url());
        const evil = await page.request.post(BASE + '/media', { multipart: { csrf, next: '//evil.example/', file: { name: 'tile2.wav', mimeType: 'audio/wav', buffer: wav } } });
        const tagged = await page.request.post(BASE + '/media', { multipart: { csrf, tag: 'Fresh', file: { name: 'tagged.wav', mimeType: 'audio/wav', buffer: wav } } });
        check('an upload from a tag of the soundboard gets the tag', tagged.ok() && (await page.request.get(BASE + '/sounds?q=tagged')).ok()
            && (await (await page.request.get(BASE + '/tags?q=fresh')).json()).some((h) => h.name === 'fresh' && !h.new));
        check('an upload ignores a foreign next', evil.ok() && /^http:\/\/localhost:8000\/media\?batch=[\w-]+&edit=\d+/.test(evil.url()), evil.url());

        await page.goto(BASE + '/admin/audit');
        t = await text(page);
        check('trash in the audit log', ['task.restore', 'media.restore', 'media.purge', 'trash.empty'].every((a) => t.includes(a)), t.slice(0, 500));
    },

    // a zip goes into the library, its files get tags at once and are reviewed one by one
    async uploads() {
        const { page } = await session('ann');
        await page.goto(BASE + '/media');
        await pickFiles(page, { name: 'pack.zip', mimeType: 'application/zip', buffer: zipOf({ 'pack/ding.wav': wav, 'pack/dong.wav': wav, 'pack/cover.png': png, '__MACOSX/pack/._ding.wav': Buffer.from('x') }) });
        let t = await text(page);
        check('zip unpacked into the library', t.includes('Uploaded 3 files') && t.includes('2 sounds, 1 image') && !t.includes('._ding'), t.slice(0, 400));
        const rows = page.locator('main .list > li');
        check('the list shows only the upload', await rows.count() === 3);
        const ask = page.locator('#sheet-upload');
        const box = page.locator('.review-batch');
        check('the upload asks what next in a sheet, not on the page', await ask.isVisible() && await box.isHidden());
        await ask.locator('input[aria-labelledby="tags-batch-sheet"]').fill('drums');
        await ask.getByRole('button', { name: 'Add tags to all 3' }).click();
        await page.waitForURL(/done=tagged/);
        t = await text(page);
        check('tags added to all files of the upload', t.includes('Tags added.') && await rows.locator('.chip.accent', { hasText: 'drums' }).count() === 3, t.slice(0, 400));
        check('after tagging the sheet asks to review', await ask.isVisible() && await ask.locator('a.primary', { hasText: 'Review one by one' }).count() === 1);

        await ask.getByRole('button', { name: 'Later' }).click();
        check('closing the sheet leaves its actions on the page', await until(() => box.isVisible()));
        await box.getByRole('link', { name: 'Review one by one' }).click();
        await settle(page);
        let sheet = page.locator('[data-edit-sheet]');
        check('review opens the first file', await sheet.isVisible() && (await sheet.textContent()).includes('File 1 of 3') && (await sheet.innerText()).includes('cover.png'));
        await sheet.locator('input[name=display_name]').fill('Cover art');
        await sheet.getByRole('button', { name: 'Save and next' }).click();
        await page.waitForURL(/done=saved/);
        await settle(page);
        sheet = page.locator('#sheet-edit');
        check('Save and next opens Edit sound of the next file', await sheet.isVisible() && (await sheet.textContent()).includes('File 2 of 3'));
        await sheet.getByRole('link', { name: 'Skip' }).click();
        await settle(page);
        check('Skip goes to the last file', await sheet.isVisible() && (await sheet.textContent()).includes('File 3 of 3') && await sheet.getByRole('button', { name: 'Save and finish' }).count() === 1);
        await sheet.getByRole('button', { name: 'Save and finish' }).click();
        await page.waitForURL((u) => !u.searchParams.has('edit'));
        t = await text(page);
        check('the review ends at the upload', t.includes('Saved.') && t.includes('Cover art') && !await page.locator('.sheet:popover-open').count(), t.slice(0, 300));
        check('after the review only a line tells the list is the upload', await box.count() === 0 && (await page.locator('.batch-line').innerText()).includes('Showing the 3 files of this upload.'));

        const ding = await rows.filter({ hasText: 'ding.wav' }).locator('[data-preview-media]').first().getAttribute('data-preview-media');
        await page.goto(BASE + '/tasks/new?guild=1&play=' + ding);
        const step = page.locator('.step', { has: page.locator('input[data-step-upload]') });
        await step.locator('input[data-step-upload]').setInputFiles({ name: 'clap.wav', mimeType: 'audio/wav', buffer: wav });
        check('a sound uploaded in the task editor joins the play step', await until(async () => await step.locator('.tok', { hasText: 'clap.wav' }).count() === 1)
            && await step.locator('.tok input[type=hidden]').count() === 2);
        check('the uploaded sound is in Media', (await (await page.request.get(BASE + '/sounds?q=clap')).json()).some((h) => h.name === 'clap.wav'));
    },

    async paging() {
        const { page } = await session('ann');
        const rows = () => page.locator('main .list > li');
        await page.goto(BASE + '/messages');
        check('first page has 25 messages and a pager', await rows().count() === 25 && await page.locator('.pager a[rel=next]').count() === 1
            && await page.locator('.pager a[rel=prev]').count() === 0);
        await page.click('.pager a[rel=next]');
        check('next page in place', await until(async () => page.url().includes('page=2') && (await page.locator('.pager .hint').innerText()) === 'Page 2' && await page.locator('.pager a[rel=prev]').count() === 1));
        await page.goBack();
        check('back goes to the first page', await until(async () => !page.url().includes('page=') && await page.locator('.pager a[rel=prev]').count() === 0));
        await page.fill('main input[name=q]', 'bulk 1');
        await until(async () => page.url().includes('q=bulk') && await rows().count() === 10);
        const t = await text(page);
        check('search starts on page 1 and keeps the term', !page.url().includes('page=') && await page.inputValue('main input[name=q]') === 'bulk 1'
            && await rows().count() === 10 && t.includes('Bulk 10') && !t.includes('Bulk 20') && await page.locator('.pager').count() === 0, t.slice(0, 300));
        await page.goto(BASE + '/messages?q=no%20such%20thing');
        check('empty search result', (await text(page)).includes('Nothing matches "no such thing".'));
    },

    async promoted() {
        const { page } = await session('bob');
        await page.goto(BASE + '/admin');
        check('same session is admin now', (await text(page)).includes('BOT_ADMINS') && (await text(page)).includes('carol.smith'));
    },

    // rows seeded by run.sh: Mira pending, ann approved with a session, spam blocked, kim denied after 7 days
    async access() {
        const anon = await (await browser.newContext()).newPage();
        await anon.goto(BASE + '/');
        check('Discord login with a client secret', await anon.getByRole('link', { name: 'Log in with Discord' }).count() === 1);
        await anon.goto(BASE + '/login/discord');
        check('Discord login needs the bot', (await anon.locator('main').innerText()).includes('once the bot is connected'));

        const { page } = await session('ann');
        await page.goto(BASE + '/admin/access');
        let t = await text(page);
        check('pending badge in the settings menu', await page.locator('.set-nav a[href="/admin/access"] .badge').innerText() === '1');
        check('access tab lists the request', t.includes('Mira') && t.includes('@mira.k') && t.includes('I run the raid nights.') && t.includes('denied tomorrow')
            && t.includes('Pending 1') && t.includes('Approved 1') && t.includes('Blocked 1') && t.includes('Denied 1'), t.slice(0, 600));
        const submit = async (user, action) => {
            const nav = page.waitForNavigation();
            await page.locator(`form[action="/admin/access/${user}"]`).evaluate((f, a) => f.requestSubmit(f.querySelector(`button[value="${a}"]`)), action);
            await nav;
        };
        await page.locator('input[name=guild][value="2"][form="access-111111111111111111"]').uncheck();
        await submit('111111111111111111', 'approve');
        t = await text(page);
        check('approve a subset of the servers', t.includes('Approved. They can log in now.') && t.includes('Pending 0') && t.includes('Approved 2'), t.slice(0, 400));
        await page.goto(BASE + '/admin/access?state=approved&q=mira');
        t = await text(page);
        check('search on the approved tab', t.includes('Mira') && !t.includes('@ann') && t.includes('Can manage'), t.slice(0, 400));
        await page.goto(BASE + '/admin/access?state=denied');
        check('auto denied request', (await text(page)).includes('Denied automatically'));
        await page.goto(BASE + '/admin/access?state=blocked');
        await submit('222222222222222222', 'unblock');
        check('unblock', (await text(page)).includes('Unblocked.') && (await text(page)).includes('Blocked 0'));
        await page.goto(BASE + '/admin/audit?category=access');
        t = await text(page);
        check('access in the audit log', ['access.approve', 'access.unblock', 'access.autodeny'].every((a) => t.includes(a)), t.slice(0, 600));

        const discord = await browser.newContext();
        await discord.addCookies([{ name: '__Host-session', value: 'e2e-discord-session', domain: 'localhost', path: '/', secure: true, httpOnly: true, sameSite: 'Lax' }]);
        const d = await discord.newPage();
        d.on('pageerror', (e) => consoleErrors.push(e.message));
        lastPage = d;
        await d.goto(BASE + '/');
        t = await d.locator('main').innerText();
        check('Discord user sees the Home overview', t.includes('Overview') && t.includes('Next 24 hours'), t.slice(0, 300));
        check('Discord user named like an admin is no admin', await d.locator('a[href="/admin"]').count() === 0
            && await d.locator('.side-body a[href="/tasks"], .side-body a[href="/messages"], .side-body a[href="/media"], .side-body a[href="/trash"], .rail a[href="/servers"]').count() === 5);
        for (const path of ['/admin', '/admin/access']) {
            const res = await d.goto(BASE + path);
            check(`Discord session blocked from ${path}`, res.status() === 403);
        }
        const only = async (path, mine, others) => {
            const res = await d.goto(BASE + path);
            const body = await d.locator('main').innerText();
            check(`Discord user list ${path}`, res.status() === 200 && mine.every((s) => body.includes(s)) && !others.some((s) => body.includes(s)), body.slice(0, 400));
        };
        await only('/tasks', ['Guild one task', 'Every minute'], ['Guild two task']);
        await only('/messages?q=Guild', ['Guild one msg', 'Guild two msg'], []);
        await only('/media?q=guild', ['guild-one.ogg', 'guild-two.ogg'], []);
        await only('/trash', ['Trash one', 'Trash two'], []);
        await d.goto(BASE + '/tasks');
        await d.getByRole('link', { name: 'Guild one task' }).first().click();
        check('Discord user opens a task of server 1', (await d.locator('main').innerText()).includes('Guild one task'));
        await page.goto(BASE + '/tasks?q=Guild+two');
        const otherTask = await page.getByRole('link', { name: 'Guild two task' }).first().getAttribute('href');
        check('Discord user gets 404 for a task of server 2', (await d.goto(BASE + otherTask)).status() === 404, otherTask);

        await d.goto(BASE + '/tasks?q=Shared+horn');
        await d.getByRole('link', { name: 'Shared horn task' }).first().click();
        t = await d.locator('main').innerText();
        check('Discord user sees a sound without server group in the task', (await d.content()).includes('shared-horn.ogg') && !t.includes('Deleted sound'), t.slice(0, 600));
        await d.click('main .btns button[value=save]');
        check('Discord user saves a task with a sound without server group', (await text(d)).includes('Saved.'));

        await page.goto(BASE + '/media?q=shared-horn');
        await page.locator('main .list a[href*="edit="]').first().click();
        const sheet = page.locator('#sheet-edit:popover-open');
        await sheet.waitFor();
        await sheet.locator('select[name=guild]').selectOption('2');
        await sheet.locator('input[name=group_only]').check();
        await sheet.locator('.btns button[type=submit]').click();
        await until(async () => (await text(page)).includes('only this server'));
        t = await text(page);
        check('Cloudron user groups a sound with server 2, only there', t.includes('shared-horn.ogg') && t.includes('only this server'), t.slice(0, 600));
        await d.goto(BASE + '/media?server=2');
        t = await d.locator('main').innerText();
        check('Discord user filters media by server group', t.includes('shared-horn.ogg') && t.includes('guild-two.ogg') && !t.includes('guild-one.ogg'), t.slice(0, 600));
        await page.goto(BASE + '/admin/audit?category=media');
        check('server group in the audit log', (await text(page)).includes('not suggested to other servers'));

        await page.goto(BASE + '/admin/access?state=approved');
        await submit('333333333333333334', 'revoke');
        check('revoke', (await text(page)).includes('Access revoked.'));
        await d.goto(BASE + '/');
        check('revoke logs the Discord user out', await d.getByRole('link', { name: 'Log in with Cloudron' }).count() === 1);
    },

    // store and README screenshots, run with SCREENSHOTS=1
    async shots() {
        const { ctx, page } = await session('ann');
        const shot = async (path, name, opts = {}) => {
            await page.goto(BASE + path);
            await page.evaluate(() => document.fonts.ready);
            await page.screenshot({ path: `/work/shots/${name}.png`, fullPage: true, ...opts });
        };
        fs.mkdirSync('/work/shots', { recursive: true });
        await page.setViewportSize({ width: 1280, height: 900 });
        await shot('/', 'dashboard');
        await shot('/media', 'media');
        await shot('/messages/1', 'message');
        await page.goto(BASE + '/tasks/new');
        await page.fill('input[name=name]', 'Adhan');
        await page.fill('input[name=times]', '05:00 11:50 14:00 16:35 18:15');
        for (const t of ['join', 'play', 'message', 'wait', 'leave']) await page.click(`button[value="add:${t}"]`);
        await page.screenshot({ path: '/work/shots/task.png', fullPage: true });
        await page.setViewportSize({ width: 390, height: 844 });
        await shot('/', 'dashboard-phone', { fullPage: false });
        await shot('/messages/1', 'message-phone');
        await ctx.close();
    },

    async persisted() {
        const { page } = await session('ann');
        await page.goto(BASE + '/media');
        const t = await text(page);
        check('session survives restart', await page.locator('#app').count() === 1);
        check('media survive restart', t.includes('friday_card.png') && t.includes('beep.ogg'));
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Every minute', exact: true }).click();
        check('missed runs in the history', (await page.locator('[data-live=runs]').innerText()).includes('Missed, the app was not running.'));
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Greeting', exact: true }).click();
        check('event task in the editor', await page.locator('input[name=kind][value=event]').isChecked() && await page.inputValue('input[name=cooldown]') === '45s'
            && await page.locator('select[name="s0.to"]').inputValue() === 'trigger' && await page.locator('.picker.dm-member').isHidden());
        check('event task summary', (await page.locator('main').innerText()).includes('Runs when the event happens.'));
        const greeting = page.url();
        await page.goto(BASE + '/tasks?q=greet');
        check('task search', await page.locator('main .list > li').count() === 1 && await page.getByRole('link', { name: 'Greeting', exact: true }).count() === 1);
        await page.goto(greeting);
        const toks = () => page.locator('.tokens .tok input[type=hidden]').evaluateAll((els) => els.map((e) => e.value).join());
        const count = () => page.locator('[data-count]').innerText();
        check('member tokens rendered', await toks() === '333333333333333333' && await count() === '1 of 25');
        await page.route('**/members?*', (route) => route.fulfill({ json: [
            { id: '111111111111111111', label: 'Jane (@jane.doe)', name: 'Jane', username: 'jane.doe' },
            { id: '333333333333333333', label: 'Old (@old)', name: 'Old', username: 'old' }] }));
        const search = page.locator('.tokens input[type=search]');
        await search.pressSequentially('j');
        await page.locator('.tokens .suggestions li').first().waitFor();
        check('picked members not suggested', await page.locator('.tokens .suggestions li').count() === 1);
        await page.keyboard.press('Enter');
        check('enter adds a token', await toks() === '333333333333333333,111111111111111111' && await count() === '2 of 25' && await search.inputValue() === '');
        await page.keyboard.press('Backspace');
        check('backspace removes the last token', await toks() === '333333333333333333');
        await search.pressSequentially('j');
        await page.locator('.tokens .suggestions li').first().waitFor();
        await page.keyboard.press('Enter');
        await page.locator('.tok button[aria-label="Remove 333333333333333333"]').click();
        check('x removes a token', await toks() === '111111111111111111' && page.url().includes('/tasks/'));
        await page.unroute('**/members?*');
        await page.click('main .btns button[value=save]');
        await settle(page);
        await page.goto(BASE + '/tasks');
        await page.getByRole('link', { name: 'Greeting', exact: true }).click();
        await settle(page);
        check('member tokens saved', await toks() === '111111111111111111', await toks());

        await page.click('button[value="add:play"]');
        const sounds = page.locator('[data-tokens="s1.media"]');
        const soundToks = () => sounds.locator('.tok input[type=hidden]').evaluateAll((els) => els.map((e) => e.value));
        const total = () => page.locator('[data-total]').innerText();
        for (let i = 0; i < 2; i++) {
            await sounds.locator('input[type=search]').pressSequentially('bee');
            await sounds.locator('.suggestions li').first().waitFor();
            await page.keyboard.press('Enter');
        }
        let ids = await soundToks();
        check('sound picked twice', ids.length === 2 && ids[0] === ids[1] && await sounds.locator('xpath=..').locator('[data-count]').innerText() === '2 of 25', ids.join());
        check('total of a sequence', /^Total 0:0[34] \/ 10:00$/.test(await total()), await total());
        await page.locator('label:has(> input[name="s1.order"][value=random])').click();
        check('total of a random pick', /^Total 0:0[12] \/ 10:00$/.test(await total()), await total());
        const pv = sounds.locator('.tok .pv').first();
        await pv.click();
        const slider = sounds.locator('.tok input[type=range]');
        check('preview shows the volume slider', await slider.count() === 1 && await slider.inputValue() === '100' && await pv.getAttribute('aria-pressed') === 'true'
            && await sounds.locator('.tok .nm').first().isHidden());
        const saved = page.waitForResponse((r) => r.url().endsWith('/prefs/preview-volume'));
        await slider.fill('35');
        check('preview volume saved', (await saved).status() === 204);
        await slider.waitFor({ state: 'detached' });
        check('the end of the preview restores the name', await sounds.locator('.tok .nm').first().isVisible());
        await page.click('main .btns button[value=save]');
        check('play step saved', (await text(page)).includes('Saved.'), (await text(page)).slice(0, 300));
        ids = await soundToks();
        check('sounds and preview volume after reload', ids.length === 2 && await page.locator('input[name="s1.order"][value=random]').isChecked()
            && await page.locator('body').getAttribute('data-preview-vol') === '35', ids.join());
        const hits = await (await page.request.get(BASE + '/sounds?q=beep')).json();
        check('sound search answers JSON', hits.length === 1 && hits[0].name === 'beep.ogg' && hits[0].duration_ms > 1000, JSON.stringify(hits));
        await page.goto(BASE + '/media');
        check('media shows multi sound usage', (await text(page)).includes('used by task Greeting'));
    },
};

try {
    await steps[step]();
} catch (e) {
    check('step finished', false, e.message.split('\n')[0]);
}
const csp = consoleErrors.filter((e) => /Content Security Policy|Refused/.test(e));
check('no CSP violations', csp.length === 0, csp.join(' | '));
check('no script errors', consoleErrors.length === 0, consoleErrors.join(' | '));
if (failures > 0 && lastPage) {
    fs.writeFileSync(`/work/fail-${step}.html`, await lastPage.content());
    console.log(`       page saved to fail-${step}.html (${lastPage.url()})`);
}
await browser.close();
process.exit(failures > 0 ? 1 : 0);
