// Time picker with iOS-style scroll wheels.
//
// The page renders a small container (see the "time-picker" template):
//   - a hidden <input name="due_time"> holding the value the form submits
//     ("14:30", or "" for no time)
//   - a button that shows the current choice and opens the panel
//   - a panel with three empty wheels (hour, minute, AM/PM) that are filled
//     in each time the panel opens
//
// All event handlers are attached to the document once, so they also work for
// pickers that htmx adds to the page later (like the ones in the edit form).
(function () {
    'use strict';

    var MINUTE_STEP = 5; // the minute wheel counts 0, 5, 10, ... 55
    var DEFAULT_TIME = { hour: 8, minute: 0, ampm: 'AM' };

    function pad(n) {
        return String(n).padStart(2, '0');
    }

    // "14:30" -> { hour: 2, minute: 30, ampm: 'PM' }. Empty or invalid -> null.
    function parse(value) {
        var m = /^(\d{2}):(\d{2})$/.exec(value);
        if (!m) return null;
        var h24 = Number(m[1]);
        var minute = Number(m[2]);
        if (h24 > 23 || minute > 59) return null;
        return { hour: h24 % 12 || 12, minute: minute, ampm: h24 >= 12 ? 'PM' : 'AM' };
    }

    // { hour: 2, minute: 30, ampm: 'PM' } -> "14:30"
    function toValue(t) {
        var h24 = (t.hour % 12) + (t.ampm === 'PM' ? 12 : 0);
        return pad(h24) + ':' + pad(t.minute);
    }

    // { hour: 2, minute: 30, ampm: 'PM' } -> "2:30 PM"
    function toLabel(t) {
        return t.hour + ':' + pad(t.minute) + ' ' + t.ampm;
    }

    function parts(picker) {
        return {
            input: picker.querySelector('input[type="hidden"]'),
            trigger: picker.querySelector('.time-trigger'),
            panel: picker.querySelector('.time-panel'),
            hour: picker.querySelector('[data-wheel="hour"]'),
            minute: picker.querySelector('[data-wheel="minute"]'),
            ampm: picker.querySelector('[data-wheel="ampm"]')
        };
    }

    // Store a time (or null for "no time") in the hidden input and the button.
    function setValue(picker, t) {
        var p = parts(picker);
        p.input.value = t ? toValue(t) : '';
        p.trigger.textContent = t ? toLabel(t) : 'No time';
    }

    // Make the button text match whatever the hidden input currently holds.
    function showCurrent(picker) {
        setValue(picker, parse(parts(picker).input.value));
    }

    function fill(wheel, items) {
        wheel.textContent = '';
        items.forEach(function (item) {
            var el = document.createElement('div');
            el.className = 'wheel-item';
            el.dataset.value = String(item.value);
            el.textContent = item.label;
            wheel.appendChild(el);
        });
    }

    function build(picker, current) {
        var p = parts(picker);

        var hours = [];
        for (var h = 1; h <= 12; h++) {
            hours.push({ value: h, label: String(h) });
        }

        var minutes = [];
        for (var m = 0; m < 60; m += MINUTE_STEP) {
            minutes.push(m);
        }
        // A saved time that isn't on the grid keeps its own minute, so opening the
        // picker on it never silently changes it.
        if (minutes.indexOf(current.minute) === -1) {
            minutes.push(current.minute);
            minutes.sort(function (a, b) { return a - b; });
        }

        fill(p.hour, hours);
        fill(p.minute, minutes.map(function (v) { return { value: v, label: pad(v) }; }));
        fill(p.ampm, [{ value: 'AM', label: 'AM' }, { value: 'PM', label: 'PM' }]);
    }

    function indexOfValue(wheel, value) {
        var i = Array.prototype.findIndex.call(wheel.children, function (el) {
            return el.dataset.value === String(value);
        });
        return i < 0 ? 0 : i;
    }

    // Which item is in the middle of the wheel? The CSS pads each wheel so that
    // scrolling by exactly one item height moves the next item into the middle.
    function selectedIndex(wheel) {
        var itemHeight = wheel.firstElementChild.offsetHeight;
        var i = Math.round(wheel.scrollTop / itemHeight);
        return Math.max(0, Math.min(i, wheel.children.length - 1));
    }

    function scrollToIndex(wheel, index, smooth) {
        var itemHeight = wheel.firstElementChild.offsetHeight;
        wheel.scrollTo({ top: index * itemHeight, behavior: smooth ? 'smooth' : 'auto' });
    }

    // Highlight the item sitting in the middle of the wheel.
    function mark(wheel) {
        var current = wheel.querySelector('.is-selected');
        var next = wheel.children[selectedIndex(wheel)];
        if (current === next) return;
        if (current) current.classList.remove('is-selected');
        next.classList.add('is-selected');
    }

    function readWheels(picker) {
        var p = parts(picker);
        return {
            hour: Number(p.hour.children[selectedIndex(p.hour)].dataset.value),
            minute: Number(p.minute.children[selectedIndex(p.minute)].dataset.value),
            ampm: p.ampm.children[selectedIndex(p.ampm)].dataset.value
        };
    }

    function open(picker) {
        closeAll(picker);
        var p = parts(picker);
        var t = parse(p.input.value) || DEFAULT_TIME;

        build(picker, t);
        p.panel.hidden = false;
        p.trigger.setAttribute('aria-expanded', 'true');

        // Moving the wheels to their starting positions fires scroll events. Those
        // are not the person choosing something, so ignore them for a moment.
        picker._settling = true;
        scrollToIndex(p.hour, indexOfValue(p.hour, t.hour), false);
        scrollToIndex(p.minute, indexOfValue(p.minute, t.minute), false);
        scrollToIndex(p.ampm, indexOfValue(p.ampm, t.ampm), false);
        [p.hour, p.minute, p.ampm].forEach(mark);
        setTimeout(function () { picker._settling = false; }, 250);

        // Opening the picker means "I want a time", so what the wheels show
        // counts straight away. The "No time" button clears it again.
        setValue(picker, t);
    }

    function close(picker) {
        var p = parts(picker);
        p.panel.hidden = true;
        p.trigger.setAttribute('aria-expanded', 'false');
    }

    function closeAll(except) {
        document.querySelectorAll('.time-picker').forEach(function (picker) {
            if (picker !== except) close(picker);
        });
    }

    // Scrolling a wheel: highlight the middle item right away, and save the
    // choice once the wheel has stopped moving for a moment. Scroll events don't
    // bubble, so this listens in the capture phase.
    document.addEventListener('scroll', function (event) {
        var wheel = event.target;
        if (!(wheel instanceof Element) || !wheel.classList.contains('wheel')) return;
        if (!wheel.firstElementChild) return;

        mark(wheel);

        var picker = wheel.closest('.time-picker');
        if (picker._settling) return;
        clearTimeout(wheel._timer);
        wheel._timer = setTimeout(function () {
            setValue(picker, readWheels(picker));
        }, 120);
    }, true);

    document.addEventListener('click', function (event) {
        var target = event.target;

        var trigger = target.closest('.time-trigger');
        if (trigger) {
            var picker = trigger.closest('.time-picker');
            if (parts(picker).panel.hidden) {
                open(picker);
            } else {
                close(picker);
            }
            return;
        }

        // Clicking an item scrolls it into the middle.
        var item = target.closest('.wheel-item');
        if (item) {
            var wheel = item.parentElement;
            scrollToIndex(wheel, Array.prototype.indexOf.call(wheel.children, item), true);
            return;
        }

        var owner = target.closest('.time-picker');
        if (target.closest('.time-clear')) {
            setValue(owner, null);
            close(owner);
            return;
        }
        if (target.closest('.time-done')) {
            close(owner);
            return;
        }

        // A click anywhere else closes any open panel.
        closeAll(owner);
    });

    document.addEventListener('keydown', function (event) {
        if (event.key === 'Escape') closeAll(null);
    });

    // The add form calls reset() after a todo is saved. A hidden input isn't
    // restored by reset, so put the picker back to its starting value by hand.
    document.addEventListener('reset', function (event) {
        event.target.querySelectorAll('.time-picker').forEach(function (picker) {
            parts(picker).input.value = picker.dataset.default;
            showCurrent(picker);
            close(picker);
        });
    });

    function initAll(root) {
        root.querySelectorAll('.time-picker').forEach(showCurrent);
    }

    initAll(document);
    if (window.htmx) {
        // Runs for the first page load and again for every piece of content htmx swaps in.
        htmx.onLoad(initAll);
    }
})();