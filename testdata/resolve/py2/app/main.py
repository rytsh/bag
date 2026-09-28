from pkg import Widget, util
from pkg import util as u2
from pkg.sub import deep
from nsp import mod
import pkg.util as pu
from pkg.base import Widget as W

def run():
    Widget.build()
    util.tool()
    u2.other()
    deep.dive()
    mod.nsfunc()
    W.build()
    w = Widget()
    w.render()

class Runner:
    def go(self):
        Widget.build()
        util.tool()
