// Each top-level figure is a lightbox group, styled by $gallery.css.
// Without JavaScript, figure links open the original images.
// The optional .download-all button saves the page's figure images as a ZIP.
document.addEventListener('DOMContentLoaded', () => {
    const groups = [...document.querySelectorAll('figure')]
        .filter((figure) => !figure.parentElement.closest('figure'));
    const galleries = groups.filter((figure) => figure.classList.contains('gallery'));

    function tiles(group) {
        const boxes = group.classList.contains('gallery') ? [...group.querySelectorAll(':scope > figure')] : [group];
        return boxes
            .map((box) => ({
                box,
                link: box.querySelector(':scope > a'),
                caption: box.querySelector(':scope > figcaption'),
            }))
            .filter((tile) => tile.link);
    }

    // The CSS phantom absorbs final-row space with a 25% fallback. Size it so
    // those tiles match the preceding row. Re-running is safe because the
    // phantom cannot change tile breaks or the preceding row's height.
    function sizeLastRow(gallery) {
        const reset = () => {
            gallery.style.removeProperty('--phantom-basis');
            gallery.style.removeProperty('--phantom-grow');
        };
        const rows = new Map();
        for (const { box } of tiles(gallery)) {
            const top = box.offsetTop;
            if (!rows.has(top)) rows.set(top, []);
            rows.get(top).push(box);
        }
        // Let CSS handle one-row or hidden galleries.
        if (rows.size < 2) return reset();
        const tops = [...rows.keys()].sort((a, b) => a - b);
        const lastRow = rows.get(tops[tops.length - 1]);
        const height = rows.get(tops[tops.length - 2])[0].getBoundingClientRect().height;
        let sumRatios = 0;
        for (const box of lastRow) {
            const ratio = parseFloat(box.style.getPropertyValue('--aspect-ratio'));
            if (!ratio) return reset(); // keep CSS fallback for unknown ratios
            sumRatios += ratio;
        }
        // The phantom needs its own leading gap, even with a zero basis.
        const gap = parseFloat(getComputedStyle(gallery).columnGap) || 0;
        const basis = gallery.clientWidth - sumRatios * height - gap * lastRow.length;
        gallery.style.setProperty('--phantom-basis', Math.max(basis, 0) + 'px');
        gallery.style.setProperty('--phantom-grow', '0');
    }

    galleries.forEach(sizeLastRow);

    // Numbering across the page keeps names from different groups distinct.
    // /foo/ and /foo/index.html both name downloads foo.
    const segment = location.pathname.replace(/(^|\/)index\.html$/, '').split('/').filter(Boolean).pop() ?? '';
    let pageName = segment.replace(/\.html$/, '');
    try { pageName = decodeURIComponent(pageName); } catch { /* keep a malformed escape as written */ }
    pageName = pageName.trim() || location.hostname || document.title.trim();
    const downloadNames = new Map();
    groups.flatMap(tiles).forEach(({ link }, i) => {
        const ext = link.pathname.match(/\.[^./]*$/)?.[0] ?? '';
        downloadNames.set(link, `${pageName} ${String(i + 1).padStart(3, '0')}${ext}`);
    });

    const downloadAll = document.querySelector('.download-all');
    const downloadAllButton = downloadAll?.querySelector('button');
    if (downloadAllButton && downloadNames.size > 0) {
        const label = `Download all ${downloadNames.size} images`;
        downloadAllButton.textContent = label;
        downloadAll.hidden = false;
        downloadAllButton.addEventListener('click', async () => {
            downloadAllButton.disabled = true;
            try {
                // Fetch sequentially to limit memory use; browsers may store blobs on disk.
                const files = [];
                for (const [link, name] of downloadNames) {
                    downloadAllButton.textContent = `Downloading ${files.length + 1} of ${downloadNames.size}…`;
                    const response = await fetch(link.href);
                    if (!response.ok) throw new Error(`${link.href}: ${response.status}`);
                    files.push({ name, blob: await response.blob() });
                }
                downloadAllButton.textContent = 'Saving…';
                save(await zip(files), `${pageName}.zip`);
                downloadAllButton.textContent = label;
            } catch (err) {
                console.error(err);
                downloadAllButton.textContent = err instanceof RangeError ? err.message : 'Download failed. Try again';
            } finally {
                downloadAllButton.disabled = false;
            }
        });
    }

    function save(blob, name) {
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = name;
        document.body.appendChild(a);
        a.click();
        a.remove();
        // Revoking at once can cancel the download.
        setTimeout(() => URL.revokeObjectURL(a.href), 60000);
    }

    let crcTable;
    function crc32(bytes) {
        crcTable ??= Array.from({ length: 256 }, (_, n) => {
            for (let k = 0; k < 8; k++) n = n & 1 ? 0xEDB88320 ^ (n >>> 1) : n >>> 1;
            return n >>> 0;
        });
        let crc = 0xFFFFFFFF;
        for (let i = 0; i < bytes.length; i++) crc = crcTable[(crc ^ bytes[i]) & 0xFF] ^ (crc >>> 8);
        return (crc ^ 0xFFFFFFFF) >>> 0;
    }

    // ZIP entries are uncompressed because JPEGs gain little from compression.
    // Without ZIP64, offsets and sizes must fit in 32 bits.
    async function zip(files) {
        const encoder = new TextEncoder();
        const now = new Date();
        const time = now.getHours() << 11 | now.getMinutes() << 5 | now.getSeconds() >> 1;
        const date = (now.getFullYear() - 1980) << 9 | (now.getMonth() + 1) << 5 | now.getDate();
        const parts = [];
        const entries = [];
        let offset = 0;
        for (const { name, blob } of files) {
            const nameBytes = encoder.encode(name);
            const crc = crc32(new Uint8Array(await blob.arrayBuffer()));
            // The local and central headers share these fields, from
            // "version needed to extract" to "extra field length".
            const shared = (view, at) => {
                view.setUint16(at, 20, true);
                view.setUint16(at + 2, 0x0800, true); // UTF-8 name
                view.setUint16(at + 4, 0, true); // stored
                view.setUint16(at + 6, time, true);
                view.setUint16(at + 8, date, true);
                view.setUint32(at + 10, crc, true);
                view.setUint32(at + 14, blob.size, true);
                view.setUint32(at + 18, blob.size, true);
                view.setUint16(at + 22, nameBytes.length, true);
            };
            const local = new DataView(new ArrayBuffer(30 + nameBytes.length));
            local.setUint32(0, 0x04034B50, true);
            shared(local, 4);
            new Uint8Array(local.buffer).set(nameBytes, 30);
            const entry = new DataView(new ArrayBuffer(46 + nameBytes.length));
            entry.setUint32(0, 0x02014B50, true);
            entry.setUint16(4, 20, true); // version made by
            shared(entry, 6);
            entry.setUint32(42, offset, true);
            new Uint8Array(entry.buffer).set(nameBytes, 46);
            parts.push(local, blob);
            entries.push(entry);
            offset += local.byteLength + blob.size;
        }
        const size = entries.reduce((sum, e) => sum + e.byteLength, 0);
        if (offset + size > 0xFFFFFFFF) throw new RangeError('Too large for one zip file');
        const end = new DataView(new ArrayBuffer(22));
        end.setUint32(0, 0x06054B50, true);
        end.setUint16(8, entries.length, true);
        end.setUint16(10, entries.length, true);
        end.setUint32(12, size, true);
        end.setUint32(16, offset, true);
        return new Blob([...parts, ...entries, end], { type: 'application/zip' });
    }

    const lightboxResizers = [];
    let relayout = false;
    window.addEventListener('resize', () => {
        if (relayout) return;
        relayout = true;
        requestAnimationFrame(() => {
            relayout = false;
            galleries.forEach(sizeLastRow);
            lightboxResizers.forEach((resize) => resize());
        });
    });

    groups.forEach((gallery) => {
        let openLightbox;
        gallery.addEventListener('click', (event) => {
            // Preserve browser actions for modified clicks.
            if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
            const link = event.target.closest('a');
            const group = tiles(gallery);
            const index = group.findIndex((tile) => tile.link === link);
            if (index < 0) return;
            const images = group.map((tile) => tile.link.querySelector('img'));
            if (!images.every((image) => image && image.src)) return;
            event.preventDefault();
            // A tile's caption element wins over its image's title and alt.
            const captions = group.map((tile, i) =>
                (tile.caption ? tile.caption.textContent : images[i].title || images[i].alt).trim());
            openLightbox ??= buildLightbox(images, captions, group.map((tile) => tile.link));
            openLightbox(index);
        });
    });

    function refreshImages(lightbox) {
        // Safari may retain a smaller srcset candidate after a viewport resize.
        lightbox.querySelectorAll('img[srcset]').forEach((image) => {
            image.sizes = '101vw';
            image.sizes = '100vw';
        });
    }

    function createButton(parent, cls, text, label) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = cls;
        button.textContent = text;
        button.setAttribute('aria-label', label);
        parent.appendChild(button);
        return button;
    }

    // The tile links open the full-size images, which the download link saves.
    function buildLightbox(images, captions, links) {
        const lightbox = document.createElement('dialog');
        lightbox.className = 'lightbox';
        lightbox.setAttribute('aria-label', 'Image gallery');
        document.body.appendChild(lightbox);

        const prevBtn = createButton(lightbox, 'lightbox-prev', '❮', 'Previous image');
        const nextBtn = createButton(lightbox, 'lightbox-next', '❯', 'Next image');
        prevBtn.hidden = nextBtn.hidden = images.length < 2;
        const closeBtn = createButton(lightbox, 'lightbox-close', '×', 'Close gallery');
        closeBtn.autofocus = true;

        const downloadLink = document.createElement('a');
        downloadLink.className = 'lightbox-download';
        downloadLink.setAttribute('aria-label', 'Download image');
        downloadLink.innerHTML = '<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" ' +
            'stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
            '<path d="M12 4v11M7 10l5 5 5-5M5 20h14"/></svg>';
        lightbox.appendChild(downloadLink);

        const scroller = document.createElement('div');
        scroller.className = 'lightbox-scroller';
        lightbox.appendChild(scroller);

        images.forEach((image, i) => {
            const slide = document.createElement('figure');
            slide.className = 'lightbox-slide';
            scroller.appendChild(slide);

            const lbImage = document.createElement('img');
            lbImage.alt = image.alt;
            lbImage.loading = 'lazy';
            lbImage.sizes = '100vw';
            if (image.srcset) lbImage.srcset = image.srcset;
            lbImage.src = image.src;
            slide.appendChild(lbImage);

            const text = captions[i];
            if (text) {
                const caption = document.createElement('figcaption');
                caption.className = 'lightbox-caption';
                caption.textContent = text;
                slide.appendChild(caption);
            }
        });

        let currentIndex = 0;
        function setIndex(index) {
            currentIndex = Math.min(Math.max(index, 0), images.length - 1);
            prevBtn.disabled = currentIndex === 0;
            nextBtn.disabled = currentIndex === images.length - 1;
            downloadLink.href = links[currentIndex].href;
            downloadLink.download = downloadNames.get(links[currentIndex]);
        }

        function goTo(index, behavior = 'auto') {
            setIndex(index);
            scroller.scrollTo({ left: scroller.clientWidth * currentIndex, behavior });
        }

        function settle() {
            if (lightbox.open && scroller.clientWidth) {
                setIndex(Math.round(scroller.scrollLeft / scroller.clientWidth));
            }
        }
        // Older Safari lacks scrollend; treat a pause in scrolling as its end.
        if ('onscrollend' in scroller) {
            scroller.addEventListener('scrollend', settle);
        } else {
            let scrollTimer;
            scroller.addEventListener('scroll', () => {
                clearTimeout(scrollTimer);
                scrollTimer = setTimeout(settle, 150);
            });
        }
        prevBtn.addEventListener('click', () => goTo(currentIndex - 1));
        nextBtn.addEventListener('click', () => goTo(currentIndex + 1));
        closeBtn.addEventListener('click', () => lightbox.close());
        scroller.addEventListener('click', (event) => {
            if (event.target.tagName !== 'IMG') lightbox.close();
        });

        let cursorTimer;
        lightbox.addEventListener('mousemove', () => {
            lightbox.style.cursor = '';
            clearTimeout(cursorTimer);
            cursorTimer = setTimeout(() => { lightbox.style.cursor = 'none'; }, 2000);
        });
        lightbox.addEventListener('close', () => {
            clearTimeout(cursorTimer);
            lightbox.style.cursor = '';
        });
        // Listen on the document because disabling a focused button moves
        // focus out of the dialog.
        document.addEventListener('keydown', (event) => {
            if (!lightbox.open) return;
            if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
                goTo(currentIndex + (event.key === 'ArrowLeft' ? -1 : 1));
                event.preventDefault();
            }
        });

        // Scroll snap alone may not keep the slide, as when a phone rotates.
        lightboxResizers.push(() => {
            if (!lightbox.open) return;
            refreshImages(lightbox);
            goTo(currentIndex, 'instant');
        });

        return (index) => {
            lightbox.showModal();
            goTo(index, 'instant');
            refreshImages(lightbox);
        };
    }
});
