# Builds the final report: renders the D2 diagrams and compiles report.tex.
#
#   make            # figures (if changed) and report.pdf
#   make figures    # only the diagrams
#   make clean      # remove the PDF's by-products
#   make distclean  # also remove report.pdf and the rendered figures
#
# Needs:
#   * d2        go install oss.terrastruct.com/d2@latest
#   * magick    ImageMagick built with librsvg (Fedora's is)
#   * python3   for svgfix.py, which makes d2's SVG render properly in librsvg
#   * Liberation Sans (optional, see FONT_DIR)
#   * tectonic  or set TEXBUILD="pdflatex -interaction=nonstopmode -halt-on-error"
#               and run make twice for the table of contents
#
# The diagrams go d2 -> SVG -> PNG. d2 0.7.1 can write PNG itself, but it
# downloads a Playwright driver for that from an address that no longer exists.

D2      ?= d2
D2FLAGS ?= --theme 0 --pad 20 --layout elk --elk-nodeNodeBetweenLayers 34 \
           --elk-edgeNodeBetweenLayers 16 --elk-padding "[top=34,left=26,bottom=26,right=26]"
MAGICK  ?= magick

# The diagrams' font. d2 measures text with these files and svgfix.py points
# the SVG at the same installed font, so labels fit their boxes. Without the
# files the diagrams still build, in whatever font the system falls back to.
FONT_NAME ?= Liberation Sans
FONT_DIR  ?= /usr/share/fonts/liberation-sans-fonts
ifneq ($(wildcard $(FONT_DIR)/LiberationSans-Regular.ttf),)
D2FONTS := --font-regular $(FONT_DIR)/LiberationSans-Regular.ttf --font-bold $(FONT_DIR)/LiberationSans-Bold.ttf \
           --font-italic $(FONT_DIR)/LiberationSans-Italic.ttf
SVGFIX  := python3 svgfix.py --font "$(FONT_NAME)"
else
D2FONTS :=
SVGFIX  := python3 svgfix.py
endif
DENSITY ?= 192
TEXBUILD ?= tectonic -X compile

DIAGRAMS := context container container-support component dynamic state deployment
FIGS     := $(DIAGRAMS:%=figures/%.png)
SECTIONS := $(wildcard sections/*.tex)

.PHONY: all figures clean distclean
all: report.pdf

report.pdf: report.tex $(SECTIONS) $(FIGS) $(wildcard figures/*)
	$(TEXBUILD) report.tex

figures: $(FIGS)

figures/%.svg: ../diagrams/%.d2 svgfix.py
	@mkdir -p figures
	$(D2) $(D2FLAGS) $(D2FONTS) $< $@.tmp.svg
	$(SVGFIX) $@.tmp.svg $@
	@rm -f $@.tmp.svg

figures/%.png: figures/%.svg
	$(MAGICK) -density $(DENSITY) -background white $< -flatten $@

clean:
	rm -f report.aux report.log report.out report.toc figures/*.svg

distclean: clean
	rm -f report.pdf $(FIGS)
