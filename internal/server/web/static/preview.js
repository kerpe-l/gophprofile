// Превью выбранного файла: изображение показывается до отправки формы.
(function () {
    "use strict";

    var input = document.getElementById("file");
    var preview = document.getElementById("preview");

    if (!input || !preview) {
        return;
    }

    var current = "";

    function release() {
        if (current) {
            URL.revokeObjectURL(current);
            current = "";
        }
    }

    input.addEventListener("change", function () {
        release();

        var file = input.files && input.files[0];
        if (!file) {
            preview.removeAttribute("src");
            preview.hidden = true;

            return;
        }

        current = URL.createObjectURL(file);
        preview.src = current;
        preview.hidden = false;
    });

    window.addEventListener("pagehide", release);
})();
