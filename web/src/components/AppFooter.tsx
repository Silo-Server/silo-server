import { SiloBrand } from "@/components/SiloBrand";
import { useBranding } from "@/hooks/useBranding";

/**
 * The popcorn mark that sits under the wordmark. Its paths were copied from the
 * project owner's own weflix frontend so both products close a page the same
 * way (see THIRD_PARTY_NOTICES.md). Decorative: the wordmark above it carries
 * the server's name.
 */
function PopcornMark() {
  return (
    <svg
      width="22"
      height="28"
      viewBox="0 0 42 48"
      aria-hidden="true"
      className="mt-2.5 shrink-0"
      xmlns="http://www.w3.org/2000/svg"
    >
      <path
        d="M10.2693 38.2524L7.68896 18.5139C8.60882 19.2949 9.85756 19.8396 11.1372 20.2506L13.1485 40.6469C11.972 40.0404 10.8478 39.1308 10.2693 38.2524Z"
        fill="white"
      ></path>
      <path
        d="M31.6422 38.2524C31.0639 39.1308 29.9397 40.0404 28.7632 40.6469L30.7745 20.2506C32.0541 19.8396 33.3029 19.2949 34.2227 18.5139L31.6422 38.2524Z"
        fill="white"
      ></path>
      <path
        d="M26.7617 41.4977C24.8139 42.1421 23.6816 42.3419 22.0605 42.501L22.3131 21.5415C24.104 21.4901 26.4217 21.2475 28.3963 20.826L26.7617 41.4977Z"
        fill="white"
      ></path>
      <path
        d="M13.5154 20.826L15.15 41.4977C17.0978 42.1421 18.2301 42.3419 19.8512 42.501L19.5986 21.5415C17.8077 21.4901 15.49 21.2475 13.5154 20.826Z"
        fill="white"
      ></path>
      <path
        d="M7.19186 13.4953C7.30819 15.9662 13.4294 17.9247 20.9557 17.8818C27.5289 17.8443 33.0122 16.2922 34.3553 14.253C34.0492 13.913 33.6265 13.6797 33.1504 13.6165C33.1555 13.5565 33.1588 13.496 33.1585 13.4346C33.153 12.4575 32.436 11.6528 31.5019 11.5027C31.5266 11.3763 31.5395 11.2456 31.5387 11.1119C31.5326 10.0236 30.6451 9.14657 29.5567 9.15273C29.5321 9.15288 29.5081 9.15588 29.4836 9.15693C29.5532 8.95342 29.5923 8.73595 29.5911 8.50888C29.5848 7.42066 28.6975 6.54359 27.6091 6.54974C27.3593 6.55124 27.1217 6.60092 26.9022 6.68692C26.5909 6.10101 25.9932 5.69264 25.2962 5.64551C25.1748 4.66835 24.3399 3.9145 23.3307 3.9202C22.6956 3.9238 22.1339 4.22876 21.7762 4.69716C21.4134 4.30245 20.8934 4.05482 20.3151 4.05812C19.2267 4.06427 18.3494 4.95155 18.3556 6.03962C18.3568 6.24748 18.3906 6.44724 18.4512 6.63499C18.007 6.71738 17.6165 6.94821 17.331 7.27433C17.1043 6.43028 16.3335 5.81 15.4183 5.8151C14.5633 5.82006 13.8407 6.36965 13.5723 7.1322C12.8459 7.42591 12.3347 8.13834 12.3395 8.96933C12.3405 9.15393 12.3687 9.33162 12.4172 9.50091C12.1723 9.39375 11.9022 9.33372 11.6176 9.33537C10.7594 9.34033 10.0346 9.89382 9.76844 10.6609C9.51326 10.5423 9.22956 10.4749 8.9295 10.4767C7.84107 10.4829 6.9637 11.37 6.97 12.4582C6.97196 12.8171 7.07148 13.152 7.2408 13.4408C7.22353 13.4582 7.20837 13.4773 7.19186 13.4953Z"
        fill="#FFD700"
      ></path>
      <path
        clipRule="evenodd"
        d="M31.5387 11.1114C31.538 10.9792 31.5237 10.8504 31.4982 10.7258C34.2616 11.762 36.1411 13.2199 35.9888 14.8259C35.9814 14.9332 32.8005 37.704 32.8005 37.704C32.4336 41.3104 27.1955 44.1928 20.9559 44.2286C14.7163 44.1928 9.47829 41.3104 9.11127 37.704C9.11127 37.704 5.93037 14.9332 5.92301 14.8259C5.84631 14.0229 6.28237 13.4715 7.00303 12.7876C7.04446 13.0219 7.12461 13.2427 7.2408 13.4408C7.23043 13.4511 7.22082 13.4622 7.21122 13.4733C7.20483 13.4806 7.19846 13.488 7.19186 13.4951C7.30834 15.9662 13.4295 17.9245 20.9559 17.8815C27.5289 17.8438 33.0123 16.2917 34.3553 14.2524C34.0492 13.9123 33.6265 13.6791 33.1504 13.6159C33.1555 13.556 33.1588 13.4954 33.1585 13.4342C33.153 12.4568 32.436 11.6521 31.5018 11.5022C31.5265 11.3758 31.5395 11.2451 31.5387 11.1114ZM10.2695 38.2524L7.689 18.5139C8.60886 19.2949 9.8576 19.8396 11.1373 20.2506L13.1485 40.6469C11.972 40.0404 10.8478 39.1308 10.2695 38.2524ZM31.6424 38.2524C31.0639 39.1308 29.9398 40.0404 28.7632 40.6469L30.7745 20.2506C32.0542 19.8396 33.3029 19.2949 34.2227 18.5139L31.6424 38.2524ZM26.7617 41.4977C24.8141 42.1421 23.6817 42.3419 22.0605 42.501L22.3131 21.5415C24.1041 21.4901 26.4217 21.2474 28.3965 20.826L26.7617 41.4977ZM13.5154 20.826L15.1501 41.4977C17.0978 42.1421 18.2301 42.3419 19.8513 42.501L19.5986 21.5415C17.8077 21.4901 15.4901 21.2474 13.5154 20.826Z"
        fill="#FA320A"
        fillRule="evenodd"
      ></path>
    </svg>
  );
}

/**
 * The footer that closes the scrollable pages in the app shell: the surface
 * fades into the page background, the wordmark sits centred, and the copyright
 * line closes the page off. A tagline set in server branding is appended to
 * that line, so an admin gives the footer a voice without a code change.
 *
 * The wordmark and the name come from server branding, so a deployment shows
 * its own identity here instead of a hardcoded one; an unbranded server falls
 * back to the bundled Silo wordmark and name.
 */
export function AppFooter() {
  const { serverName, footerTagline } = useBranding();
  // Two lines: the copyright, then the tagline under it. A tagline that shares
  // the copyright's line reads as one long sentence; set apart, it reads as the
  // footer's own voice.
  const year = new Date().getFullYear();

  return (
    <footer className="from-background/0 to-background text-muted-foreground mt-[30px] bg-gradient-to-b px-10 pb-10 text-sm max-md:mt-[5px] max-md:px-5 max-md:pt-10 max-md:pb-[30px]">
      <div className="mx-auto flex max-w-[1400px] flex-col items-center gap-5">
        <SiloBrand className="h-10 w-[94px] max-md:h-8 max-md:w-[75px]" />
        <PopcornMark />
        <div className="pt-2.5 text-center text-[0.8rem]">
          <p>© {year} {serverName}.</p>
          {footerTagline ? <p>{footerTagline}</p> : null}
        </div>
      </div>
    </footer>
  );
}
