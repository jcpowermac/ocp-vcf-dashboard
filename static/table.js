// table.js — Client-side sorting and filtering for data-table elements.
// Auto-attaches to all <table class="data-table"> on page load and after
// HTMX SSE swaps so live-updated tables retain interactive behavior.
(function () {
    "use strict";

    // parseValue extracts a sortable value from cell text.
    function parseValue(text) {
        text = text.trim();
        if (text === "" || text === "--:--:--") return null;

        // Percentage: "85%" → 85
        if (/^[\d.]+%$/.test(text)) return parseFloat(text);

        // Pure number (including decimals and negatives)
        if (/^-?[\d,]+\.?\d*$/.test(text)) return parseFloat(text.replace(/,/g, ""));

        // Duration HH:MM:SS
        if (/^\d{2}:\d{2}:\d{2}$/.test(text)) {
            var p = text.split(":");
            return parseInt(p[0]) * 3600 + parseInt(p[1]) * 60 + parseInt(p[2]);
        }

        // Age strings: "3d 2h", "45m", "2h 10m", "just now", "never"
        if (text === "never") return Infinity;
        if (text === "just now") return 0;
        var ageSeconds = 0;
        var ageMatch = text.match(/(\d+)d/);
        if (ageMatch) ageSeconds += parseInt(ageMatch[1]) * 86400;
        ageMatch = text.match(/(\d+)h/);
        if (ageMatch) ageSeconds += parseInt(ageMatch[1]) * 3600;
        ageMatch = text.match(/(\d+)m/);
        if (ageMatch) ageSeconds += parseInt(ageMatch[1]) * 60;
        ageMatch = text.match(/(\d+)s/);
        if (ageMatch) ageSeconds += parseInt(ageMatch[1]);
        if (ageSeconds > 0) return ageSeconds;

        // Version strings like "4.18", "5.0" — sort numerically
        if (/^\d+\.\d+$/.test(text)) return parseFloat(text);

        // Date strings like "Mar 05 14:30"
        var d = Date.parse(text);
        if (!isNaN(d)) return d;

        // Default: lowercase string for lexicographic sort
        return text.toLowerCase();
    }

    // getCellText returns the visible text of a table cell.
    function getCellText(td) {
        return (td.textContent || td.innerText || "").trim();
    }

    // initTable attaches sort headers and filter row to a data-table.
    function initTable(table) {
        if (table.dataset.tableInit === "1") return;
        table.dataset.tableInit = "1";

        var thead = table.querySelector("thead");
        var tbody = table.querySelector("tbody");
        if (!thead || !tbody) return;

        var headerRow = thead.querySelector("tr");
        if (!headerRow) return;
        var ths = headerRow.querySelectorAll("th");
        if (ths.length === 0) return;

        // State
        var sortCol = -1;
        var sortDir = 0; // 0=none, 1=asc, 2=desc
        var filters = new Array(ths.length);
        for (var i = 0; i < filters.length; i++) filters[i] = "";

        // Make headers clickable for sorting
        ths.forEach(function (th, idx) {
            th.style.cursor = "pointer";
            th.style.userSelect = "none";
            var indicator = document.createElement("span");
            indicator.className = "sort-indicator";
            indicator.textContent = "";
            th.appendChild(indicator);

            th.addEventListener("click", function () {
                if (sortCol === idx) {
                    sortDir = (sortDir + 1) % 3;
                } else {
                    sortCol = idx;
                    sortDir = 1;
                }
                updateIndicators();
                applySort();
            });
        });

        // Create filter row
        var filterRow = document.createElement("tr");
        filterRow.className = "filter-row";
        ths.forEach(function (th, idx) {
            var td = document.createElement("td");
            var input = document.createElement("input");
            input.type = "text";
            input.className = "column-filter";
            input.placeholder = "Filter...";
            input.setAttribute("aria-label", "Filter " + getCellText(th));
            input.addEventListener("input", function () {
                filters[idx] = input.value.toLowerCase();
                applyFilter();
            });
            td.appendChild(input);
            filterRow.appendChild(td);
        });
        thead.appendChild(filterRow);

        function updateIndicators() {
            ths.forEach(function (th, idx) {
                var ind = th.querySelector(".sort-indicator");
                if (!ind) return;
                if (idx === sortCol && sortDir === 1) {
                    ind.textContent = " \u25B2";
                } else if (idx === sortCol && sortDir === 2) {
                    ind.textContent = " \u25BC";
                } else {
                    ind.textContent = "";
                }
            });
        }

        function applySort() {
            if (sortDir === 0) {
                // Restore original order — re-apply filter only
                applyFilter();
                return;
            }
            var rows = Array.from(tbody.querySelectorAll("tr:not(.no-match)"));
            rows.sort(function (a, b) {
                var cellA = a.children[sortCol];
                var cellB = b.children[sortCol];
                if (!cellA || !cellB) return 0;
                var va = parseValue(getCellText(cellA));
                var vb = parseValue(getCellText(cellB));
                if (va === null && vb === null) return 0;
                if (va === null) return 1;
                if (vb === null) return -1;
                var cmp = 0;
                if (typeof va === "number" && typeof vb === "number") {
                    cmp = va - vb;
                } else {
                    va = String(va);
                    vb = String(vb);
                    cmp = va < vb ? -1 : va > vb ? 1 : 0;
                }
                return sortDir === 2 ? -cmp : cmp;
            });
            rows.forEach(function (row) {
                tbody.appendChild(row);
            });
        }

        function applyFilter() {
            var rows = tbody.querySelectorAll("tr");
            rows.forEach(function (row) {
                if (row.classList.contains("filter-row")) return;
                var show = true;
                for (var i = 0; i < filters.length; i++) {
                    if (filters[i] === "") continue;
                    var cell = row.children[i];
                    if (!cell) { show = false; break; }
                    var text = getCellText(cell).toLowerCase();
                    if (text.indexOf(filters[i]) === -1) {
                        show = false;
                        break;
                    }
                }
                row.style.display = show ? "" : "none";
                if (show) {
                    row.classList.remove("no-match");
                } else {
                    row.classList.add("no-match");
                }
            });
            // Re-sort visible rows
            if (sortDir > 0) applySort();
        }
    }

    // initAll finds and initializes all data-tables in the document.
    function initAll() {
        document.querySelectorAll("table.data-table").forEach(initTable);
    }

    // Initialize on DOM ready
    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", initAll);
    } else {
        initAll();
    }

    // Re-initialize after HTMX SSE swaps replace table content.
    // The SSE swap replaces innerHTML of the container, destroying
    // the old table and creating a new one that needs initialization.
    // Note: we call initAll (not removeAttribute + initTable) so that
    // the data-table-init guard prevents duplicate filter rows when
    // multiple htmx:afterSwap events fire in quick succession.
    document.body.addEventListener("htmx:afterSwap", function () {
        setTimeout(initAll, 50);
    });
})();
