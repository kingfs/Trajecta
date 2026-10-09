/*
 * The DOM animation feature set, in its own module so `LazyMotion` can fetch it
 * as a separate chunk. Imported statically it would add ~28 kB gzipped to the
 * entry bundle, which is the whole cost of `motion` for what is currently one
 * indicator; loaded lazily it arrives after first paint and the indicator simply
 * jumps into place until it does.
 */
export { domAnimation as default } from "motion/react";
