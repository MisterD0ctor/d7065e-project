# Build the final report (final_report.tex; figures in figures/, rendered from
# docs/diagrams/*.d2 with `d2 --pad 20 X.d2 ../report/figures/X.pdf`).
# Without a TeX installation, `tectonic final_report.tex` builds it too.
# `make DOC=final_report_template` builds the course's empty template.
#
# Usage:
#   make            # build the PDF
#   make clean      # remove LaTeX intermediate files
#   make distclean  # also remove the PDF

PDFLATEX := pdflatex -interaction=nonstopmode -halt-on-error
DOC      ?= final_report

.PHONY: all clean distclean
all: $(DOC).pdf
	@echo "==> $(DOC).pdf ready"

$(DOC).pdf: $(DOC).tex
	$(PDFLATEX) $(DOC).tex
	$(PDFLATEX) $(DOC).tex
	@rm -f $(DOC).aux $(DOC).log $(DOC).out $(DOC).toc

clean:
	rm -f $(DOC).aux $(DOC).log $(DOC).out $(DOC).toc

distclean: clean
	rm -f $(DOC).pdf
