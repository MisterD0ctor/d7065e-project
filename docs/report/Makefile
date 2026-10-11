# Build the final report (final_report.tex) and its diagrams.
# `make DOC=final_report_template` builds the course's empty template.
#
# Usage:
#   make            # render the diagrams that changed, then build the PDF
#   make figures    # only the diagrams
#   make clean      # remove LaTeX intermediate files
#   make distclean  # also remove the PDF
#
# The diagrams are docs/diagrams/*.d2, rendered to figures/*.png by way of SVG:
# d2 -> svgfix.py -> ImageMagick. d2 can write PDF and PNG itself, but only
# with a Playwright download that d2 0.7.1 can no longer fetch on a fresh
# machine. The text sizes in the .d2 files are chosen so that every label
# prints at about 7 pt or more at the widths final_report.tex uses: after
# changing a diagram, look at the figure in the PDF, not only on screen.
#
# Needs d2, ImageMagick built with librsvg, python3, and pdflatex or tectonic.

DOC ?= final_report

D2      ?= d2
D2FLAGS ?= --pad 10 --layout elk --elk-nodeNodeBetweenLayers 20 \
           --elk-edgeNodeBetweenLayers 10 --elk-padding "[top=34,left=14,bottom=14,right=14]"
MAGICK  ?= magick
DENSITY ?= 240

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

DIAGRAMS := context container container-support component dynamic state deployment
FIGS     := $(DIAGRAMS:%=figures/%.png)

PDFLATEX      := pdflatex -interaction=nonstopmode -halt-on-error
HAVE_PDFLATEX := $(shell command -v pdflatex 2>/dev/null)

.PHONY: all figures clean distclean
all: $(DOC).pdf
	@echo "==> $(DOC).pdf ready"

ifeq ($(DOC),final_report)
$(DOC).pdf: $(FIGS) $(wildcard figures/*.png)
endif

$(DOC).pdf: $(DOC).tex
ifdef HAVE_PDFLATEX
	$(PDFLATEX) $(DOC).tex
	$(PDFLATEX) $(DOC).tex
	@rm -f $(DOC).aux $(DOC).log $(DOC).out $(DOC).toc
else
	tectonic -X compile $(DOC).tex
endif

figures: $(FIGS)

figures/%.svg: ../diagrams/%.d2 svgfix.py
	@mkdir -p figures
	$(D2) $(D2FLAGS) $(D2FONTS) $< $@.tmp.svg
	$(SVGFIX) $@.tmp.svg $@
	@rm -f $@.tmp.svg

figures/%.png: figures/%.svg
	$(MAGICK) -density $(DENSITY) -units PixelsPerInch -background white $< -flatten -alpha off -strip $@

clean:
	rm -f $(DOC).aux $(DOC).log $(DOC).out $(DOC).toc

distclean: clean
	rm -f $(DOC).pdf
