const getTypes = el => new Set([
    ...(el?.getAttributeNS?.('http://www.idpf.org/2007/ops', 'type')?.split(' ') ?? []),
    ...(el?.attributes?.getNamedItem?.('epub:type')?.value?.split(' ') ?? []),
])
const getRoles = el => new Set(el?.getAttribute?.('role')?.split(' '))

const isSuper = el => {
    if (el.matches('sup')) return true
    const { verticalAlign } = getComputedStyle(el)
    return verticalAlign === 'super'
        || verticalAlign === 'top'
        || verticalAlign === 'text-top'
        || /^\d/.test(verticalAlign)
}

const refTypes = ['biblioref', 'glossref', 'noteref']
const refRoles = ['doc-biblioref', 'doc-glossref', 'doc-noteref']
const isFootnoteReference = a => {
    const types = getTypes(a)
    const roles = getRoles(a)
    return {
        yes: refRoles.some(r => roles.has(r)) || refTypes.some(t => types.has(t)),
        maybe: () => !types.has('backlink') && !roles.has('doc-backlink')
            && (isSuper(a) || a.children.length === 1 && isSuper(a.children[0])
            || isSuper(a.parentElement)),
    }
}

const getReferencedType = el => {
    const types = getTypes(el)
    const roles = getRoles(el)
    return roles.has('doc-biblioentry') || types.has('biblioentry') ? 'biblioentry'
        : roles.has('definition') || types.has('glossdef') ? 'definition'
        : roles.has('doc-endnote') || types.has('endnote') || types.has('rearnote') ? 'endnote'
        : roles.has('doc-footnote') || types.has('footnote') ? 'footnote'
        : roles.has('note') || types.has('note') ? 'note' : null
}

const isInline = 'a, span, sup, sub, em, strong, i, b, small, big'
const extractFootnote = (doc, anchor) => {
    let el = anchor(doc)
    const target = el
    while (el.matches(isInline)) {
        const parent = el.parentElement
        if (!parent) break
        el = parent
    }
    if (el === doc.body) {
        const sibling = target.nextElementSibling
        if (sibling && !sibling.matches(isInline)) return sibling
        throw new Error('Failed to extract footnote')
    }
    return el
}

export class FootnoteHandler extends EventTarget {
    detectFootnotes = true
    #showFragment(book, { index, anchor, check }, href) {
        const view = document.createElement('foliate-view')
        return new Promise((resolve, reject) => {
            view.addEventListener('load', e => {
                try {
                    const { doc } = e.detail
                    const el = anchor(doc)
                    const type = getReferencedType(el)
                    const hidden = el?.matches?.('aside') && type === 'footnote'
                    if (el) {
                        let range
                        if (el.startContainer) {
                            range = el
                        } else if (el.matches('li, aside')) {
                            range = doc.createRange()
                            range.selectNodeContents(el)
                        } else if (el.matches('dt')) {
                            range = doc.createRange()
                            range.setStartBefore(el)
                            let sibling = el.nextElementSibling
                            let lastDD = null
                            while (sibling && sibling.matches('dd')) {
                                lastDD = sibling
                                sibling = sibling.nextElementSibling
                            }
                            range.setEndAfter(lastDD || el)
                        } else if (el.closest('li')) {
                            range = doc.createRange()
                            range.selectNodeContents(el.closest('li'))
                        } else if (el.closest('.note')) {
                            range = doc.createRange()
                            range.selectNodeContents(el.closest('.note'))
                        } else if (el.querySelector('a')) {
                            range = doc.createRange()
                            range.setStartBefore(el)
                            let next = el.nextElementSibling
                            while (next) {
                                if (next.querySelector('a')) break
                                next = next.nextElementSibling
                            }
                            if (next) {
                                range.setEndBefore(next)
                            } else {
                                range.setEndAfter(el.parentNode.lastChild)
                            }
                            if (check && el.children.length > 3) {
                                reject(new Error('Failed to locate footnote content'))
                                return
                            }
                        } else if (check) {
                            reject(new Error('Failed to locate footnote content'))
                            return
                        } else {
                            range = doc.createRange()
                            const hasContent = el.textContent?.trim() || el.children.length > 0
                            if (!hasContent && el.parentElement) {
                                range.selectNodeContents(el.parentElement)
                            } else {
                                range.selectNode(el)
                            }
                        }
                        const frag = range.extractContents()
                        doc.body.replaceChildren()
                        doc.body.appendChild(frag)
                    }
                    const detail = { view, href, type, hidden, target: el }
                    this.dispatchEvent(new CustomEvent('render', { detail }))
                    resolve()
                } catch (e) {
                    reject(e)
                }
            })
            view.open(book)
                .then(() => this.dispatchEvent(new CustomEvent('before-render', { detail: { view } })))
                .then(() => view.goTo(index))
                .catch(reject)
        })
    }
    handle(book, e) {
        const { a, href, follow, check } = e.detail
        const { yes, maybe } = isFootnoteReference(a)
        if (yes || follow) {
            e.preventDefault()
            return Promise.resolve(book.resolveHref(href)).then(target =>
                this.#showFragment(book, target, href))
        }
        else if (this.detectFootnotes && (maybe() || check)) {
            e.preventDefault()
            return Promise.resolve(book.resolveHref(href)).then(({ index, anchor }) => {
                const target = { index, anchor: doc => extractFootnote(doc, anchor), check }
                return this.#showFragment(book, target, href)
            })
        }
    }
}
