import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useI18n } from "./i18n";

/**
 * A write that reports itself.
 *
 * Every management write in this console used to be the same twenty lines: clear
 * the previous error, set a busy flag, `await` the request, remember not to use
 * the result after unmount, set the busy flag back, and either write a message
 * into page state or forget to. Two files carried eighteen busy flags between
 * them, and the flag had to be cleared by hand in a `finally` that some paths
 * forgot.
 *
 * `useMutation` owns the pending flag and the error, and this wrapper adds the
 * two things that were being re-implemented: the queries a write invalidates, and
 * one toast per outcome. Callers keep the part that is actually theirs, which is
 * what to do with the result.
 *
 * @param mutationFn the request; usually `() => postJSON(path, payload)`
 * @param success an i18n key, or a function of the result, toasted on success.
 *   Omit it when the outcome itself decides - a request that succeeds while the
 *   job it queued reports failure reads as a failure.
 * @param error an i18n key used when the failure carries no message of its own,
 *   or a function of the error when the message needs more context than that
 * @param invalidate query keys to refetch once the write lands
 * @param onSuccess the caller's own follow-up, run after invalidation
 * @param onError the caller's own recovery, run after the toast
 */
export function useWriteMutation({ mutationFn, success, error, invalidate = [], onSuccess, onError }) {
  const queryClient = useQueryClient();
  const { t } = useI18n();

  return useMutation({
    mutationFn,
    async onSuccess(data, variables) {
      if (invalidate.length > 0) {
        await Promise.all(invalidate.map((queryKey) => queryClient.invalidateQueries({ queryKey })));
      }
      if (success) {
        toast.success(typeof success === "function" ? success(data, variables) : t(success));
      }
      onSuccess?.(data, variables);
    },
    onError(err, variables) {
      // A thrown API error carries the server's message, which is more specific
      // than any dictionary entry; the key is the fallback for a bare failure.
      const message = typeof error === "function"
        ? error(err, variables)
        : err?.message || (error ? t(error) : t("common.actionFailed"));
      toast.error(message);
      onError?.(err, variables);
    },
  });
}
