# Final report (architecture document)

`report.tex` is the report; its text is in `sections/`, one file per section.
The diagrams are D2 sources in `../diagrams/`, rendered into `figures/`.

```
make            # render the diagrams that changed, then build report.pdf
make figures    # only the diagrams
make clean      # remove by-products
make distclean  # also remove report.pdf and the rendered figures
```

Needs `d2`, ImageMagick with librsvg, `python3` and `tectonic`; the `Makefile`
says how to use `pdflatex` instead. The diagrams go d2 → SVG → `svgfix.py` →
PNG, because d2's own PNG export needs a download that no longer exists.

## Before submission

Everything still open is marked in the PDF with a red box:

```
grep -n -E '\\(todo|missingfigure)' report.tex sections/*.tex
```

That command must print nothing but the definitions near the top of `report.tex`.
Then go through the course's checklist:

- Every requirement in Section 3 is traced to a test in Section 10.
- Each design decision in Section 4.4 names a rejected alternative.
- The five diagrams are in and referenced: context, container, component,
  dynamic, deployment (and the state machine).
- The evaluation is backed by measured data.
- The document matches what was built.

## The course's template

`final_report_template.tex` and `.pdf` are the blank skeleton from the course
repository, kept for reference. The guide and the worked example are in the
course repository under `lab-assignment/`.
