# docs/handoff — messaggi cross-macchina fra VAL

Questa cartella e' TRACCIATA e viaggia con `git`: serve al VAL che riprende il lavoro su
un'altra macchina (Mac ↔ PC Windows) e non puo' vedere il disco dell'altra. E' l'unico canale
di handoff che passa dal repository.

`.handover/` resta dov'e': gitignored, privato, per-macchina, e contiene cio' che NON deve
uscire (persone, calendari, path di sessione, report grezzi). Qui va solo la parte che serve
a riprendere il lavoro tecnico.

## Regole

- **Il repo e' pubblico.** Niente nomi di persone oltre ai ruoli (VAL, ESC, CRI), niente
  email (se proprio serve un contatto: `firstcontact@alancurtisagency.com`), niente segreti,
  niente contenuto di sessioni. I path locali (`C:\Develop\...`, `/Users/.../develop/...`)
  sono ammessi: dicono su quale macchina si e' lavorato.
- **Eseguito o dedotto**, detto per ogni riga. Cio' che e' verificato porta il comando.
- Un file per giro: `YYYY-MM-DD-<slug>-<macchina>.md`, dove `<macchina>` e' `win` se lo
  scrive il VAL sul PC Windows e `mac` se lo scrive il VAL sul Mac: dal nome si sa chi parla
  e a chi. Il piu' recente e' quello da leggere; i vecchi restano come storia, non si
  riscrivono. Questo `README.md` e' l'unica eccezione al suffisso.
- Li scrive il VAL. L'ESC non tocca docs.
- Il file dice cosa e' cambiato in `main` o in quale branch, cosa e' verificato SOLO su una
  macchina, e cosa il VAL dell'altra macchina deve fare o NON fare quando riprende.
